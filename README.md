# eLMIS CommodityType Migration Tool
A standalone Go CLI utility designed to automate the migration of specific trade items (e.g., DON and non-DON) into a CommodityType hierachy within an eLMIS PostgreSQL database.

## Features
- **Smart Grouping:** Automatically groups products with identical names and matching units and skips standalone products.
- **Interactive CLI:** Prompts for approval before executing changes, allowing custom commodityType naming overrides.
- **Atomic Transactions:** Uses PostgreSQL transactions to ensure partial migagrations are rolled back if error occurs.
- **Safe Re-runs:** Exludes products that have already been tagged as commodityTypes or classified, making it safe to run multiple times,
- **UI Filtering Support:** Injects <code>{"isCommodityType":true}</code> into the extradata JSONB column of the CommodityType product so the frontend can easily filter it out of Ad-Hoc stock adjustment dropdowns, ensuring users do not manage stock using commodityTypes.

## Prerequisites
- golang

## Quick Start

1. Fork/clone repository from Github.

<code>{git clone https://github.com/Lesotho-eLMIS/commodity-seed.git}</code>
2. Add an environment file called .env to the root folder of the project, with the required postgres database credentials.
We already have sample.env file with contains sample database connection string. All you need is to run

<code>{cp sample.env .env}</code> which will copy information contained in sample.env into automatically generated .env file.

3. Edit your .env with your respective database credentials.

4. Run the project

<code>{go run main.go}</code>

