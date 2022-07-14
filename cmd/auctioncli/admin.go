package main

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/lightninglabs/protobuf-hex-display/proto"
	"github.com/lightninglabs/subasta/adminrpc"
	"github.com/urfave/cli"
)

const (
	// Complete the RFC3339 date format. (YYYY-MM-DDT00:00:00Z).
	dateFmt = "%vT00:00:00Z"

	// Financial Report csv filename. The format is type (batch, lsat, ...)
	// and date (start and end date).
	csvFileName = "%s_pool_accounting_%s.csv"

	// Financial Report zip filename.
	zipFileName = "pool_accounting_%s.zip"

	// timestampFormat is the default format for rpc timestamps.
	timestampFormat = "2006-01-02 15:04:05"
)

// formatTimestamp formats an rpc timestamp field (int64) for printing.
func formatTimestamp(timestamp int64) string {
	t := time.Unix(timestamp, 0)
	return t.Format(timestampFormat)
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
	batchEntryHeaders := []string{
		"Date-time human readable (UTC)",
		"Date-time unix time (UTC)",

		"Balance sheet",
		"Units",
		"Asset type",
		"Market price",
		"Historical accounting value",

		"Batch ID",
		"Batch TXID",
		"Batch total TX fee",
		"Total auction fees accrued",
		"Total trader TX fee share",
	}
	if err := csvwriter.Write(batchEntryHeaders); err != nil {
		return err
	}

	for _, entry := range report.BatchEntries {
		line := []string{
			formatTimestamp(entry.Timestamp),
			fmt.Sprintf("%d", entry.Timestamp),

			fmt.Sprintf("%v", entry.ProfitInSats),
			"sats",
			"BTC",
			fmt.Sprintf("%v", entry.BtcPrice.Price),
			fmt.Sprintf("%v", entry.ProfitInUsd),

			hex.EncodeToString(entry.BatchKey),
			entry.BatchTxId,
			fmt.Sprintf("%v", entry.BatchTxFees),
			fmt.Sprintf("%v", entry.AccruedFees),
			fmt.Sprintf("%v", entry.TraderChainFees),
		}

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
	lsatEntryHeaders := []string{
		"Date-time human readable (UTC)",
		"Date-time unix time (UTC)",

		"Balance sheet",
		"Units",
		"Asset type",
		"Auction market",
		"Market price",
		"Historical accounting value",
	}
	if err := csvwriter.Write(lsatEntryHeaders); err != nil {
		return err
	}

	for _, entry := range report.LsatEntries {
		line := []string{
			formatTimestamp(entry.Timestamp),
			fmt.Sprintf("%d", entry.Timestamp),

			fmt.Sprintf("%v", entry.ProfitInSats),
			"sats",
			"BTC",
			"LSAT",
			fmt.Sprintf("%v", entry.BtcPrice.Price),
			fmt.Sprintf("%v", entry.ProfitInUsd),
		}

		if err := csvwriter.Write(line); err != nil {
			return err
		}
	}

	return nil
}

// generateSummaryReport generates a file with all the summary data.
func generateSummaryReport(filename string,
	report *adminrpc.FinancialReportResponse) error {

	summary := report.Summary
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed creating summary file: %s", err)
	}
	defer file.Close()

	w := bufio.NewWriter(file)

	_, err = fmt.Fprintf(w, "Creation time: %v\n", formatTimestamp(
		summary.CreationTimestamp))
	if err != nil {
		return nil
	}

	_, err = fmt.Fprintf(w, "Start date: %v\n", formatTimestamp(
		summary.StartTimestamp))
	if err != nil {
		return nil
	}

	_, err = fmt.Fprintf(w, "End date: %v\n", formatTimestamp(
		summary.EndTimestamp))
	if err != nil {
		return nil
	}

	_, err = fmt.Fprintf(w, "Closing balance: %v sats, %s USD\n",
		summary.ClosingBalance, summary.ClosingBalanceInUsd)
	if err != nil {
		return nil
	}

	_, err = fmt.Fprintf(w, "Lease batch fees: %v sats, %s USD\n",
		summary.LeaseBatchFees, summary.LeaseBatchFeesInUsd)
	if err != nil {
		return nil
	}

	_, err = fmt.Fprintf(w, "LSAT: %v sats, %s USD\n",
		summary.Lsat, summary.LsatInUsd)
	if err != nil {
		return nil
	}

	_, err = fmt.Fprintf(w, "Chain fees: %v sats, %s USD\n",
		summary.ChainFees, summary.ChainFeesInUsd)
	if err != nil {
		return nil
	}

	_, err = fmt.Fprintf(w, "net revenue: %v sats, %s USD\n",
		summary.NetRevenue, summary.NetRevenueInUsd)
	if err != nil {
		return nil
	}

	w.Flush()
	return nil
}

// formatFilenameDates returns the start and end date formatted to be included
// in file names.
func formatFilenameDates(start, end time.Time) string {
	// YYYY-MM-DD.
	format := "2006-01-02"
	return fmt.Sprintf("%s_to_%s", start.Format(format), end.Format(format))
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

		timeSpan := formatFilenameDates(start, end)
		// Generate csv.
		batchFilename := fmt.Sprintf(csvFileName, "batch", timeSpan)
		if err := generateBatchReport(batchFilename, report); err != nil {
			return err
		}

		lsatFilename := fmt.Sprintf(csvFileName, "lsat", timeSpan)
		if err := generateLSATReport(lsatFilename, report); err != nil {
			return err
		}

		summaryFilename := fmt.Sprintf("pool_accounting_summary_%s.txt",
			timeSpan)
		err = generateSummaryReport(summaryFilename, report)
		if err != nil {
			return err
		}

		zipName := fmt.Sprintf(zipFileName, timeSpan)
		err = generateZip(
			zipName, batchFilename, lsatFilename, summaryFilename,
		)
		if err != nil {
			return nil
		}

		// Remove intermediate files.
		if err = removeFiles(
			batchFilename, lsatFilename, summaryFilename,
		); err != nil {
			return err
		}

		fmt.Printf("Report written to %s\n", zipName)

		return nil
	},
}

var shutdownCommand = cli.Command{
	Name:        "shutdown",
	Usage:       "Shutdown the whole subasta server",
	Description: `Shutdown the whole subasta server.`,
	Action: wrapSimpleCmd(func(ctx context.Context, _ *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.Shutdown(ctx, &adminrpc.EmptyRequest{})
	}),
}

var setStatusCommand = cli.Command{
	Name:        "setstatus",
	Usage:       "Set a new health/readiness status",
	Description: `Set a new health/readiness status on the k8s endpoint.`,
	ArgsUsage:   "status_name",
	Action: wrapSimpleCmd(func(ctx context.Context, cliCtx *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.SetStatus(ctx, &adminrpc.SetStatusRequest{
			ServerState: cliCtx.Args().First(),
		})
	}),
}

var setLogLevelCommand = cli.Command{
	Name:        "setloglevel",
	Usage:       "Set a new server wide log level",
	Description: `Set a new server wide log level.`,
	ArgsUsage:   "loglevel",
	Action: wrapSimpleCmd(func(ctx context.Context, cliCtx *cli.Context,
		client adminrpc.AuctionAdminClient) (proto.Message, error) {

		return client.SetLogLevel(ctx, &adminrpc.SetLogLevelRequest{
			LogLevel: cliCtx.Args().First(),
		})
	}),
}
