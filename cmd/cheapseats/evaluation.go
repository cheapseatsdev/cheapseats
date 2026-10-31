package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/cheapseatsdev/cheapseats/internal/env"
	"github.com/cheapseatsdev/cheapseats/internal/evaluation"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/store"
)

func evaluationCmd(ctx context.Context, args []string) error {
	if len(args) == 3 && args[0] == "export" {
		db, err := store.Open(env.Or("DATA_DIR", "data"))
		if err != nil {
			return err
		}
		defer db.Close()
		d, err := evaluation.New(db.DB).Dataset(ctx, args[1])
		if err != nil {
			return err
		}
		b, err := d.JSONL()
		if err != nil {
			return err
		}
		f, err := os.OpenFile(args[2], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(b)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		return closeErr
	}
	if len(args) < 1 || args[0] != "import" || len(args) > 2 {
		return errors.New("usage: cheapseats evaluation import [file.jsonl] | export <dataset-id> <new-file.jsonl>")
	}
	var data []byte
	var err error
	source := "bundled golden fixtures"
	if len(args) == 1 {
		data, err = grade.BundledGolden()
	} else {
		source = args[1]
		f, openErr := os.Open(source)
		if openErr != nil {
			return openErr
		}
		defer f.Close()
		data, err = io.ReadAll(io.LimitReader(f, evaluation.MaxImportBytes+1))
	}
	if err != nil {
		return err
	}
	db, err := store.Open(env.Or("DATA_DIR", "data"))
	if err != nil {
		return err
	}
	defer db.Close()
	a, err := evaluation.New(db.DB).ImportJSONL(ctx, source, data)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(a)
}
