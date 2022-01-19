package main

import (
	"archive/zip"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/lightninglabs/protobuf-hex-display/proto"
	"github.com/lightninglabs/subasta/adminrpc"

	"github.com/urfave/cli"
)

const (
	// Complete the RFC3339 date format
	dateFmt = "%vT00:00:00Z"

	// Financial Report csv filename
	csvFileName = "%s_pool_accounting_%d-%02d_to_%d-%02d.csv"

	// Financial Report zip filename
	zipFileName = "pool_accounting_%d-%02d_to_%d-%02d.zip"
)

var (
	// batchEntryHeaders are the batch report csv first row columns.
	batchEntryHeaders = []string{
		"creation_timestamp",
		"batch_id",
		"batch_tx_fee",
		"auctioneer_fees_accrued",
		"on_chain_trader_fees",
		"profit_sats",
		"btc_price_at_date",
		"profit_usd",
	}

	// lsatEntryHeaders are the LSAT report csv first row columns.
	lsatEntryHeaders = []string{
		"creation_timestamp",
		"profit_sats",
		"btc_price_at_date",
		"profit_usd",
	}
)

var adminCommands = []cli.Command{
	{
		Name:      "admin",
		ShortName: "adm",
		Usage:     "Server maintenance and administration.",
		Category:  "Maintenance",
		Subcommands: []cli.Command{
			mirrorDatabaseCommand,
			financialReportCommand,
		},
	},
}

var mirrorDatabaseCommand = cli.Command{
	Name:        "mirror",
	ShortName:   "m",
	Usage:       "Mirror database to SQL",
	Description: `Mirror database to SQL.`,
	Action: wrapSimpleCmd(func(ctx context.Context, _ *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.MirrorDatabase(ctx, &adminrpc.EmptyRequest{})
	}),
}

// generateBatchReport generate a csv file with all the lsat financial report data.
func generateBatchReport(filename string,
	report *adminrpc.FinancialReportResponse) error {

	csvFile, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed creating batch file: %s", err)
	}

	csvwriter := csv.NewWriter(csvFile)
	defer csvwriter.Flush()

	// Write headers.
	if err := csvwriter.Write(batchEntryHeaders); err != nil {
		return err
	}

	line := make([]string, len(batchEntryHeaders))
	for _, entry := range report.BatchEntries {
		timestamp := time.Unix(entry.Timestamp, 0)
		// creation_timestamp
		line[0] = timestamp.Format("2006-01-02 15:04:05")
		// batch_id
		line[1] = entry.BatchTxId
		// batch_tx_fee
		line[2] = fmt.Sprintf("%v", entry.AccruedFees)
		// auctioneer_fees_accrued
		line[3] = fmt.Sprintf("%v", entry.BatchTxFees)
		// on_chain_trader_fees
		line[4] = fmt.Sprintf("%v", entry.TraderChainFees)
		// profit_sats
		line[5] = fmt.Sprintf("%v", entry.ProfitInSats)
		// btc_price_at_date
		line[6] = fmt.Sprintf("%v", entry.BtcPrice.Price)
		// profit_usd
		line[7] = fmt.Sprintf("%v", entry.ProfitInUsd)

		if err := csvwriter.Write(line); err != nil {
			return err
		}
	}

	return nil
}

// generateLSATReport generate a csv file with all the lsat financial report data.
func generateLSATReport(filename string,
	report *adminrpc.FinancialReportResponse) error {

	csvFile, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed creating lsat file: %s", err)
	}

	csvwriter := csv.NewWriter(csvFile)
	defer csvwriter.Flush()

	// Write headers.
	if err := csvwriter.Write(lsatEntryHeaders); err != nil {
		return err
	}

	line := make([]string, len(lsatEntryHeaders))
	for _, entry := range report.LsatEntries {
		timestamp := time.Unix(entry.Timestamp, 0)
		// creation_timestamp
		line[0] = timestamp.Format("2006-01-02 15:04:05")
		// profit_sats
		line[1] = fmt.Sprintf("%v", entry.ProfitInSats)
		// btc_price_at_date
		line[2] = fmt.Sprintf("%v", entry.BtcPrice.Price)
		// profit_usd
		line[3] = fmt.Sprintf("%v", entry.ProfitInUsd)

		if err := csvwriter.Write(line); err != nil {
			return err
		}
	}

	return nil
}

// generateZip creates a new zip file containing all the provided file.
func generateZip(zipName string, filesNames ...string) error {

	archive, err := os.Create(zipName)
	if err != nil {
		return err
	}
	defer archive.Close()

	zipWriter := zip.NewWriter(archive)
	defer zipWriter.Close()

	for _, fileName := range filesNames {
		file, err := os.Open(fileName)
		if err != nil {
			panic(err)
		}
		defer file.Close()

		writer, err := zipWriter.Create(fileName)
		if err != nil {
			return err
		}
		if _, err := io.Copy(writer, file); err != nil {
			return err
		}
	}

	return nil
}

// removeFiles deletes the provided files.
func removeFiles(filesNames ...string) error {
	for _, fileName := range filesNames {
		if err := os.Remove(fileName); err != nil {
			return err
		}
	}

	return nil
}

var financialReportCommand = cli.Command{
	Name:        "financialreport",
	ShortName:   "fr",
	Usage:       "Generate a financial report for the given dates",
	Description: `Generate a financial report for the given dates`,

	Flags: []cli.Flag{
		cli.StringFlag{
			Name: "start_date",
			Usage: "starting date (included) for the report as " +
				"`YYYY-MM-DD`",
		},
		cli.StringFlag{
			Name: "end_date",
			Usage: "end date (excluded) for the report as " +
				"`YYYY-MM-DD`",
		},
	},
	Action: func(ctx *cli.Context) error {
		client, cleanup, err := getClient(ctx)
		if err != nil {
			return err
		}
		defer cleanup()

		// Parse parameters.
		startDate := ctx.String("start_date")
		start, err := time.Parse(
			time.RFC3339, fmt.Sprintf(dateFmt, startDate),
		)
		if err != nil {
			return fmt.Errorf("invalid start date format: %v",
				startDate)
		}

		endDate := ctx.String("end_date")
		end, err := time.Parse(
			time.RFC3339, fmt.Sprintf(dateFmt, endDate),
		)
		if err != nil {
			return fmt.Errorf("invalid end date format: %v",
				endDate)
		}

		// Get report data.
		reqCtx := context.Background()
		report, err := client.FinancialReport(
			reqCtx, &adminrpc.FinancialReportRequest{
				StartTimestamp: start.Unix(),
				EndTimestamp:   end.Unix(),
			},
		)
		if err != nil {
			return err
		}

		// Generate csv.
		batchFilename := fmt.Sprintf(csvFileName, "batch", start.Year(),
			start.Month(), end.Year(), end.Month())

		if err := generateBatchReport(batchFilename, report); err != nil {

			return err
		}

		lsatFilename := fmt.Sprintf(csvFileName, "lsat", start.Year(),
			start.Month(), end.Year(), end.Month())

		if err := generateLSATReport(lsatFilename, report); err != nil {
			return err
		}

		zipName := fmt.Sprintf(zipFileName, start.Year(), start.Month(),
			end.Year(), end.Month())

		err = generateZip(zipName, batchFilename, lsatFilename)
		if err != nil {
			return nil
		}

		// Remove intermediate files.
		if err = removeFiles(batchFilename, lsatFilename); err != nil {
			return err
		}

		return nil
	},
}
