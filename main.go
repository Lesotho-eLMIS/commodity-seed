package main

import (
	"bufio"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
)

type Product struct {
	ID                    string
	Code                  string
	FullName              string
	Description           sql.NullString // In case Description is missing
	DispensableID         string
	PackRoundingThreshold int
	NetContent            int
	RoundToZero           bool
}

func main() {

	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, relying on system environment variables")
	}

	connStr := os.Getenv("DATABASE_URL")

	db, err := sql.Open("pgx", connStr)

	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatalf("Database ping failed: %v", err)
	}

	fmt.Println("Fetching unmapped orderables...")

	// Fetch products that have NO commodityType mapping AND have not been classified as Trade Items yet
	rows, err := db.Query(`
		SELECT o.id, o.code, o.fullproductname, o.description, o.dispensableid, o.packroundingthreshold, o.netcontent, o.roundtozero
		FROM referencedata.orderables o
		WHERE NOT EXISTS (
			-- Exclude if it is already explicitly a generic commodityType
			SELECT 1 FROM referencedata.orderable_identifiers oi
			WHERE oi.orderableid = o.id AND oi.key = 'commodityType'
		)
		AND NOT EXISTS (
			-- Exclude if it has already been mapped as a specific Trade Item classification
			SELECT 1 
			FROM referencedata.orderable_identifiers oi
			JOIN referencedata.trade_item_classifications tic ON tic.tradeitemid = CAST(oi.value AS UUID)
			WHERE oi.orderableid = o.id 
			  AND oi.key = 'tradeItem'
			  AND tic.classificationsystem = 'Local_Classification'
		)
		ORDER BY o.fullproductname
	`)

	if err != nil {
		log.Fatalf("Query failed: %v", err)
	}
	defer rows.Close()

	// Group them by exactly matching Names in memory
	groupedProducts := make(map[string][]Product)
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.Code, &p.FullName, &p.Description, &p.DispensableID, &p.PackRoundingThreshold, &p.NetContent, &p.RoundToZero); err != nil {
			log.Printf("Row scan error: %v", err)
			continue
		}

		nameKey := normalizeNameForGrouping(p.FullName)
		groupedProducts[nameKey] = append(groupedProducts[nameKey], p)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("Rows iteration error: %v", err)
	}

	var cleanGroups [][]Product
	var skippedCount int

	// Evaluate each group based purely on identical names
	for _, group := range groupedProducts {
		if len(group) == 1 {
			// Skip products with no counterpart.
			// We do not create generic CommodityTypes for standalones.
			skippedCount++
			continue
		}

		// If 2 or more products share the EXACT same name, group them!
		cleanGroups = append(cleanGroups, group)
	}

	fmt.Printf("Skipped %d standalone products (no DON/non-DON pairs).\n", skippedCount)

	// Run CLI on the matched pairs
	if len(cleanGroups) > 0 {
		runInteractiveCLI(db, cleanGroups)
	} else {
		fmt.Println("No matched pairs left to process.")
	}
}

// normalizeNameForGrouping normalizes case and whitespace for exact matching
func normalizeNameForGrouping(fullName string) string {
	return strings.ToUpper(strings.TrimSpace(fullName))
}

// generateUniqueGenericCode guarantees the proposed code doesn't clash in the DB
func generateUniqueGenericCode(db *sql.DB, baseCode string) string {
	proposedCode := "COM-" + baseCode
	finalCode := proposedCode
	counter := 1

	for {
		var exists bool
		err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM referencedata.orderables WHERE code = $1)`, finalCode).Scan(&exists)
		if err != nil {
			log.Fatalf("Failed to check code uniqueness: %v", err)
		}

		if !exists {
			return finalCode
		}

		// If it exists, append a suffix and check again
		finalCode = fmt.Sprintf("%s-%d", proposedCode, counter)
		counter++
	}
}

// runInteractiveCLI handles the terminal prompts for the groups
func runInteractiveCLI(db *sql.DB, cleanGroups [][]Product) {
	reader := bufio.NewReader(os.Stdin)

	for i, group := range cleanGroups {
		p := group[0] // Use the first item to derive the group proposals
		baseCode := strings.TrimPrefix(p.Code, "DON-")

		proposedClassID := baseCode
		proposedGenericCode := generateUniqueGenericCode(db, baseCode)
		proposedGenericName := fmt.Sprintf("%s", strings.TrimSpace(p.FullName)) // Removed accidental trailing space

		fmt.Printf("\n--- Group %d of %d ---\n", i+1, len(cleanGroups))
		fmt.Printf("Item Name: %s\n", p.FullName)
		fmt.Printf("Mapped Codes (%d items):\n", len(group))
		for _, item := range group {
			fmt.Printf("  - %s\n", item.Code)
		}

		fmt.Printf("\nProposed CommodityType Name: %s\n", proposedGenericName)
		fmt.Printf("Proposed CommodityType Code: %s\n", proposedGenericCode)
		fmt.Printf("Proposed Class ID:     %s\n", proposedClassID)
		fmt.Print("Press [Enter] to accept, [S] to skip, or type custom Generic Name: ")

		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if strings.ToUpper(input) == "S" {
			fmt.Println("  Skipped.")
			continue
		}

		finalName := proposedGenericName
		if input != "" {
			finalName = input
		}

		// Execute SQL transaction for ALL items in this group
		groupSuccess := true
		for _, item := range group {
			err := executeMigration(db, item, finalName, proposedClassID, proposedGenericCode)
			if err != nil {
				fmt.Printf(" Error on %s: %v\n", item.Code, err)
				groupSuccess = false
			}
		}
		if groupSuccess {
			fmt.Println(" Group successfully migrated.")
		}
	}
}

// executeMigration handles the multi-step SQL transaction safely
func executeMigration(db *sql.DB, p Product, finalName, classID, genericCode string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. Resolve or Create the Commodity Type
	var commodityUUID string
	err = tx.QueryRow(`
		SELECT id FROM referencedata.commodity_types
		WHERE classificationid = $1 AND classificationsystem = 'Local_Classification'
	`, classID).Scan(&commodityUUID)

	if err == sql.ErrNoRows {
		err = tx.QueryRow(`
			INSERT INTO referencedata.commodity_types (id, name, classificationsystem, classificationid)
			VALUES (gen_random_uuid(), $1, 'Local_Classification', $2)
			RETURNING id
		`, finalName, classID).Scan(&commodityUUID)
		if err != nil {
			return fmt.Errorf("failed inserting commodity type: %w", err)
		}
	} else if err != nil {
		return err
	}

	// Resolve or Create the Generic Orderable
	var genericOrderableUUID string
	err = tx.QueryRow(`SELECT id FROM referencedata.orderables WHERE code = $1`, genericCode).Scan(&genericOrderableUUID)

	if err == sql.ErrNoRows {
		err = tx.QueryRow(`
    INSERT INTO referencedata.orderables (
        id, versionnumber, code, fullproductname, description, dispensableid,
        packroundingthreshold, netcontent, roundtozero, lastupdated, extradata
    ) VALUES (
        gen_random_uuid(), 1, $1, $2, $3, $4, $5, $6, $7, now(), '{"isCommodityType": true}'::jsonb
    )
    RETURNING id
`, genericCode, finalName, p.Description, p.DispensableID, p.PackRoundingThreshold, p.NetContent, p.RoundToZero).Scan(&genericOrderableUUID)
		if err != nil {
			return fmt.Errorf("failed inserting generic orderable: %w", err)
		}

		// Link generated commodityType orderable to commodityType classification
		_, err = tx.Exec(`
			INSERT INTO referencedata.orderable_identifiers (orderableid, orderableversionnumber, key, value)
			VALUES ($1, 1, 'commodityType', $2)
			ON CONFLICT DO NOTHING
		`, genericOrderableUUID, commodityUUID)
		if err != nil {
			return fmt.Errorf("failed linking generic to commodity type: %w", err)
		}

		// Clone Program and Display Category to the Generic Orderable
		_, err = tx.Exec(`
			INSERT INTO referencedata.program_orderables (
				id, programid, orderableid, orderableversionnumber,
				active, fullsupply, orderabledisplaycategoryid, displayorder
			)
			SELECT
				gen_random_uuid(), programid, $1, 1,
				true, true, orderabledisplaycategoryid, displayorder
			FROM referencedata.program_orderables
			WHERE orderableid = $2
			ON CONFLICT DO NOTHING
		`, genericOrderableUUID, p.ID)
		if err != nil {
			return fmt.Errorf("failed cloning program and display category: %w", err)
		}

		// Clone Facility Type Approvals to the Generic Orderable
		_, err = tx.Exec(`
			INSERT INTO referencedata.facility_type_approved_products (
				id, versionnumber, orderableid, programid, facilitytypeid,
				maxperiodsofstock, minperiodsofstock, emergencyorderpoint, active, lastupdated
			)
			SELECT
				gen_random_uuid(), 1, $2, programid, facilitytypeid,
				maxperiodsofstock, minperiodsofstock, emergencyorderpoint, active, now()
			FROM referencedata.facility_type_approved_products
			WHERE orderableid = $1
			ON CONFLICT DO NOTHING
		`, p.ID, genericOrderableUUID)
		if err != nil {
			return fmt.Errorf("failed cloning facility type approvals: %w", err)
		}

	} else if err != nil {
		return err
	}

	// Link Specific Orderable to TradeItem Classification
	_, err = tx.Exec(`
		INSERT INTO referencedata.trade_item_classifications (id, tradeitemid, classificationsystem, classificationid)
		SELECT gen_random_uuid(), CAST(value AS UUID), 'Local_Classification', $1
		FROM referencedata.orderable_identifiers
		WHERE key = 'tradeItem' AND orderableid = $2
		ON CONFLICT (tradeitemid, classificationsystem) DO NOTHING
	`, classID, p.ID)
	if err != nil {
		return fmt.Errorf("failed linking trade item: %w", err)
	}

	// Remove old specific products from the program
	// This sets active=false and fullsupply=false so clinics can no longer order the specific item.
	_, err = tx.Exec(`
		UPDATE referencedata.program_orderables
		SET active = false, fullsupply = false
		WHERE orderableid = $1
	`, p.ID)
	if err != nil {
		return fmt.Errorf("failed removing specific product from program: %w", err)
	}

	// Remove the old commodityType identifier from the specific product
	// This ensures the specific item acts purely as a physical Trade Item going forward.
	_, err = tx.Exec(`
		DELETE FROM referencedata.orderable_identifiers
		WHERE key = 'commodityType' AND orderableid = $1
	`, p.ID)
	if err != nil {
		return fmt.Errorf("failed removing specific commodityType tag: %w", err)
	}

	return tx.Commit()
}
