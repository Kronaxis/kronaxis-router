package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

// runGraphifyCmd handles `kronaxis-router ingest [paths...]` and
// `kronaxis-router graphify <subcommand>`.
//
// Subcommands:
//   ingest <paths...>   ingest files into pgvector (default if no subcmd)
//   reset               drop kr_chunks (use when changing embedder dim)
//   stats               print row count + token totals
func runGraphifyCmd(args []string) {
	if len(args) == 0 {
		fmt.Println("usage: kronaxis-router graphify <subcommand> [args]")
		fmt.Println("subcommands: ingest <paths...>, reset, stats")
		os.Exit(2)
	}
	switch args[0] {
	case "ingest":
		runIngest(args[1:])
	case "reset":
		runGraphifyReset()
	case "stats":
		runGraphifyStats()
	default:
		// Treat first arg as a path -- shorthand for `graphify ingest <path>`
		if _, err := os.Stat(args[0]); err == nil {
			runIngest(args)
		} else {
			fmt.Println("unknown subcommand:", args[0])
			os.Exit(2)
		}
	}
}

func runIngest(args []string) {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	configPath := fs.String("config", env("CONFIG_PATH", "config.yaml"), "path to config.yaml")
	verbose := fs.Bool("v", false, "verbose: print each file ingested")
	batchSize := fs.Int("batch", 32, "embedding batch size")
	excludesArg := fs.String("exclude", "", "comma-separated dir/file names to exclude (in addition to defaults)")
	resetFirst := fs.Bool("reset", false, "drop and recreate kr_chunks before ingesting")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	roots := fs.Args()
	if len(roots) == 0 {
		fmt.Println("usage: kronaxis-router ingest <path> [<path>...] [--config FILE] [-v] [--reset] [--exclude name1,name2]")
		os.Exit(2)
	}

	cfg2, err := loadConfig(*configPath)
	if err != nil {
		fmt.Println("load config:", err)
		os.Exit(1)
	}
	gcfg := cfg2.Graphify.WithDefaults()

	databaseURL := env("DATABASE_URL", "")
	if databaseURL == "" {
		fmt.Println("DATABASE_URL not set")
		os.Exit(1)
	}
	d, err := sql.Open("postgres", databaseURL)
	if err != nil {
		fmt.Println("open db:", err)
		os.Exit(1)
	}
	defer d.Close()
	if err := d.Ping(); err != nil {
		fmt.Println("ping db:", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	emb, err := newEmbedder(ctx, gcfg.Embedder)
	if err != nil {
		fmt.Println("embedder:", err)
		os.Exit(1)
	}
	fmt.Printf("embedder: %s (dim=%d)\n", emb.Name(), emb.Dim())

	if *resetFirst {
		if err := graphifyResetSchema(ctx, d); err != nil {
			fmt.Println("reset schema:", err)
			os.Exit(1)
		}
	}
	if err := graphifyEnsureSchema(ctx, d, emb.Dim()); err != nil {
		fmt.Println("ensure schema:", err)
		os.Exit(1)
	}

	excludes := []string{}
	if *excludesArg != "" {
		excludes = strings.Split(*excludesArg, ",")
	}
	concurrency := gcfg.IngestConcurrency
	stats, err := graphifyIngest(ctx, d, emb, IngestOpts{
		Roots:       roots,
		Excludes:    excludes,
		BatchSize:   *batchSize,
		Concurrency: concurrency,
		Verbose:     *verbose,
	})
	if err != nil {
		fmt.Printf("ingest error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("ingested in %s: %d files scanned, %d ingested, %d skipped, %d chunks, %d MB processed\n",
		stats.FinishedAt.Sub(stats.StartedAt).Round(time.Millisecond),
		stats.FilesScanned, stats.FilesIngested, stats.FilesSkipped, stats.ChunksWritten,
		stats.BytesProcessed/(1024*1024),
	)
}

func runGraphifyReset() {
	databaseURL := env("DATABASE_URL", "")
	if databaseURL == "" {
		fmt.Println("DATABASE_URL not set")
		os.Exit(1)
	}
	d, err := sql.Open("postgres", databaseURL)
	if err != nil {
		fmt.Println("open db:", err)
		os.Exit(1)
	}
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := graphifyResetSchema(ctx, d); err != nil {
		fmt.Println("reset:", err)
		os.Exit(1)
	}
	fmt.Println("kr_chunks dropped")
}

func runGraphifyStats() {
	databaseURL := env("DATABASE_URL", "")
	if databaseURL == "" {
		fmt.Println("DATABASE_URL not set")
		os.Exit(1)
	}
	d, err := sql.Open("postgres", databaseURL)
	if err != nil {
		fmt.Println("open db:", err)
		os.Exit(1)
	}
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var rowCount int64
	if err := d.QueryRowContext(ctx, "SELECT COUNT(*) FROM kr_chunks").Scan(&rowCount); err != nil {
		fmt.Println("count:", err)
		os.Exit(1)
	}
	var totalChars int64
	if err := d.QueryRowContext(ctx, "SELECT COALESCE(SUM(LENGTH(content)),0) FROM kr_chunks").Scan(&totalChars); err != nil {
		fmt.Println("sum:", err)
		os.Exit(1)
	}
	dim, _ := graphifyTableDim(ctx, d)
	fmt.Printf("kr_chunks: %d rows, %d chars total (~%d tokens), embedding dim=%d\n",
		rowCount, totalChars, totalChars/4, dim)
}
