package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/lightninglabs/protobuf-hex-display/json"
	"github.com/lightninglabs/subasta/subastadb"
	"github.com/lightninglabs/subasta/venue/matching"
)

type kv struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type diffCommand struct {
	cfg *Config
}

func (x *diffCommand) Execute(args []string) error {
	sourceBytes, err := ioutil.ReadFile(args[0])
	if err != nil {
		return fmt.Errorf("error reading source file %s: %v", args[0],
			err)
	}

	destBytes, err := ioutil.ReadFile(args[1])
	if err != nil {
		return fmt.Errorf("error reading dest file %s: %v", args[1],
			err)
	}

	var (
		source    []*kv
		dest      []*kv
		sourceMap = make(map[string][]byte)
		destMap   = make(map[string][]byte)
	)
	if err := json.Unmarshal(sourceBytes, &source); err != nil {
		return fmt.Errorf("error parsing source: %v", err)
	}
	if err := json.Unmarshal(destBytes, &dest); err != nil {
		return fmt.Errorf("error parsing dest: %v", err)
	}

	fmt.Printf("Got %d source entries and %d dest entries\n", len(source),
		len(dest))

	for _, srcEntry := range source {
		if srcEntry.Value == "" {
			continue
		}

		valBytes, err := base64.StdEncoding.DecodeString(srcEntry.Value)
		if err != nil {
			return fmt.Errorf("error decoding base64 value %s: %v",
				srcEntry.Value, err)
		}

		sourceMap[srcEntry.Key] = valBytes
	}

	for _, dstEntry := range dest {
		if dstEntry.Value == "" {
			continue
		}

		valBytes, err := base64.StdEncoding.DecodeString(dstEntry.Value)
		if err != nil {
			return fmt.Errorf("error decoding base64 value %s: %v",
				dstEntry.Value, err)
		}

		destMap[dstEntry.Key] = valBytes
	}

	fmt.Printf("Mapped to %d source entries and %d dest entries\n",
		len(source), len(dest))

	fmt.Printf("Checking source vs. dest\n")
	_ = os.MkdirAll("./diff", 0770)
	for key, value := range sourceMap {
		destValue, ok := destMap[key]
		if !ok {
			fmt.Printf("Source key '%s' not in dest map, value "+
				"is %x\n", key, value)
			continue
		}

		if !bytes.Equal(value, destValue) {
			if !strings.Contains(key, "/batch/") {
				fmt.Printf("Source key '%s' has diff!\n"+
					"Source: %x\nDest:   %x\n\n", key,
					value, destValue)

				continue
			}

			srcSnapshot, err := subastadb.DeserializeBatchSnapshot(
				bytes.NewReader(value),
			)
			if err != nil {
				return fmt.Errorf("error de-"+
					"serializing source snapshot: "+
					"%v", err)
			}

			dstSnapshot, err := subastadb.DeserializeBatchSnapshot(
				bytes.NewReader(destValue),
			)
			if err != nil {
				return fmt.Errorf("error de-"+
					"serializing source snapshot: "+
					"%v", err)
			}

			cleanup(srcSnapshot)
			cleanup(dstSnapshot)

			if reflect.DeepEqual(srcSnapshot, dstSnapshot) {
				continue
			}

			srcJson, _ := json.MarshalIndent(srcSnapshot, "", "  ")
			dstJson, _ := json.MarshalIndent(dstSnapshot, "", "  ")

			fmt.Printf("Source key '%s' has diff!\n", key)

			fileName := strings.ReplaceAll(
				key, "bitcoin/clm/subasta/mainnet/batch/", "",
			)
			err = ioutil.WriteFile(
				fmt.Sprintf("./diff/%s-source.json", fileName),
				srcJson, 0644,
			)
			if err != nil {
				return fmt.Errorf("error writing diff: %v", err)
			}

			err = ioutil.WriteFile(
				fmt.Sprintf("./diff/%s-dest.json", fileName),
				dstJson, 0644,
			)
			if err != nil {
				return fmt.Errorf("error writing diff: %v", err)
			}
		}
	}

	fmt.Printf("Checking dest vs. source\n")
	for key, value := range destMap {
		_, ok := sourceMap[key]
		if !ok {
			fmt.Printf("Dest key '%s' not in source map, value "+
				"is %x\n", key, value)
			continue
		}
	}

	return nil
}

func cleanup(sn *subastadb.BatchSnapshot) {
	sn.OrderBatch.CreationTimestamp =
		sn.OrderBatch.CreationTimestamp.Truncate(time.Millisecond)

	sort.SliceStable(sn.OrderBatch.Orders, func(i, j int) bool {
		pairI := sn.OrderBatch.Orders[i]
		pairJ := sn.OrderBatch.Orders[j]
		return compareMatchedOrders(pairI, pairJ)
	})

	for idx := range sn.OrderBatch.SubBatches {
		sort.SliceStable(sn.OrderBatch.SubBatches[idx], func(i, j int) bool {
			pairI := sn.OrderBatch.SubBatches[idx][i]
			pairJ := sn.OrderBatch.SubBatches[idx][j]
			return compareMatchedOrders(pairI, pairJ)
		})
	}

	for idx := range sn.OrderBatch.Orders {
		sortNodeAddrs(sn.OrderBatch.Orders[idx])
	}

	for i := range sn.OrderBatch.SubBatches {
		for j := range sn.OrderBatch.SubBatches[i] {
			sortNodeAddrs(sn.OrderBatch.SubBatches[i][j])
		}
	}
}

func sortNodeAddrs(o matching.MatchedOrder) {
	sort.SliceStable(o.Details.Ask.NodeAddrs, func(i, j int) bool {
		return o.Details.Ask.NodeAddrs[i].String() <
			o.Details.Ask.NodeAddrs[j].String()
	})

	sort.SliceStable(o.Details.Bid.NodeAddrs, func(i, j int) bool {
		return o.Details.Bid.NodeAddrs[i].String() <
			o.Details.Bid.NodeAddrs[j].String()
	})
}

func compareMatchedOrders(a, b matching.MatchedOrder) bool {
	hashA := sha256.New()
	var buf bytes.Buffer
	_ = subastadb.WriteElements(
		&buf, a.Details.Ask.Units, a.Details.Ask.Amt,
		a.Details.Ask.FixedRate,
		a.Details.Bid.Units, a.Details.Bid.Amt,
		a.Details.Bid.FixedRate,
	)
	_, _ = hashA.Write(buf.Bytes())
	_, _ = hashA.Write(a.Asker.AccountKey[:])
	_, _ = hashA.Write(a.Bidder.AccountKey[:])
	_, _ = hashA.Write(a.Details.Ask.Sig[:])
	_, _ = hashA.Write(a.Details.Bid.Sig[:])
	digestA := hashA.Sum(nil)

	hashB := sha256.New()
	buf.Reset()
	_ = subastadb.WriteElements(
		&buf, b.Details.Ask.Units, b.Details.Ask.Amt,
		b.Details.Ask.FixedRate,
		b.Details.Bid.Units, b.Details.Bid.Amt,
		b.Details.Bid.FixedRate,
	)
	_, _ = hashB.Write(buf.Bytes())
	_, _ = hashB.Write(b.Asker.AccountKey[:])
	_, _ = hashB.Write(b.Bidder.AccountKey[:])
	_, _ = hashB.Write(b.Details.Ask.Sig[:])
	_, _ = hashB.Write(b.Details.Bid.Sig[:])
	digestB := hashB.Sum(nil)

	return bytes.Compare(digestA, digestB) < 0
}
