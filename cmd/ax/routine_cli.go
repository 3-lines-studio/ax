package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/3-lines-studio/ax/internal/routine"
)

func runRoutineCommand(bot *botCtx, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ax routine list | ax routine run NAME | ax routine runs NAME | ax routine log NAME [RUN]")
		os.Exit(2)
	}
	set, err := routine.Open(*bot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "usage: ax routine list")
			os.Exit(2)
		}
		runner := routine.NewRunner(*bot)
		table := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(table, "NAME\tSCHEDULE\tENABLED\tLAST")
		for _, item := range set.Items {
			schedule := "-"
			if item.Schedule != nil {
				schedule = item.Schedule.String()
			}
			enabled := "no"
			if item.Enabled {
				enabled = "yes"
			}
			last := "-"
			if result, err := runner.LastRun(item.Name); err == nil {
				last = fmt.Sprintf("%s %s", result.StartedAt.Local().Format("2006-01-02 15:04"), result.Status)
			}
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", item.Name, schedule, enabled, last)
		}
		_ = table.Flush()
	case "runs":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: ax routine runs NAME")
			os.Exit(2)
		}
		item, err := set.Find(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		runs, err := routine.NewRunner(*bot).Runs(item.Name, 20)
		if err != nil {
			if errors.Is(err, routine.ErrNotFound) {
				fmt.Println("No runs yet.")
				return
			}
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		table := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(table, "ID\tSTATUS\tTRIGGER\tSTARTED\tEXIT")
		for _, run := range runs {
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%d\n", run.ID, run.Status, run.Trigger, run.StartedAt.Local().Format("2006-01-02 15:04"), run.ExitCode)
		}
		_ = table.Flush()
	case "log":
		if len(args) < 2 || len(args) > 3 {
			fmt.Fprintln(os.Stderr, "usage: ax routine log NAME [RUN]")
			os.Exit(2)
		}
		item, err := set.Find(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		runner := routine.NewRunner(*bot)
		var result routine.RunResult
		var text string
		if len(args) == 3 {
			result, text, err = runner.RunLog(item.Name, args[2])
		} else {
			result, err = runner.LastRun(item.Name)
			if err == nil {
				data, readErr := os.ReadFile(result.Log)
				if readErr == nil {
					text = string(data)
				}
			}
		}
		if err != nil {
			if errors.Is(err, routine.ErrNotFound) {
				if len(args) == 2 {
					fmt.Fprintln(os.Stderr, "no runs yet")
				} else {
					fmt.Fprintf(os.Stderr, "no run %s\n", args[2])
				}
				os.Exit(1)
			}
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Printf("%s %s (exit %d) %s\n", result.Name, result.Status, result.ExitCode, result.StartedAt.Local().Format("2006-01-02 15:04"))
		fmt.Print(text)
		if result.Status != "ok" {
			os.Exit(1)
		}
	case "run":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: ax routine run NAME")
			os.Exit(2)
		}
		item, err := set.Find(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		result, err := routine.NewRunner(*bot).Run(context.Background(), item, "manual")
		if errors.Is(err, routine.ErrRunning) {
			fmt.Fprintf(os.Stderr, "error: %s is already running\n", item.Name)
			os.Exit(1)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Printf("%s: %s (exit %d)\nlog: %s\n", result.Name, result.Status, result.ExitCode, result.Log)
		if data, err := os.ReadFile(result.Log); err == nil && len(data) > 0 {
			fmt.Print(string(data))
		}
		if result.Status != "ok" {
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: ax routine list | ax routine run NAME | ax routine runs NAME | ax routine log NAME [RUN]")
		os.Exit(2)
	}
}
