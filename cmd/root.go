package cmd

import (
	"context"
	"os"
	"path/filepath"

	"github.com/nektos/act/pkg/common"

	fswatch "github.com/andreaskoch/go-fswatch"
	"github.com/nektos/act/pkg/model"
	"github.com/nektos/act/pkg/runner"
	gitignore "github.com/sabhiram/go-gitignore"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

// Execute is the entry point to running the CLI
func Execute(ctx context.Context, version string) {
	input := new(Input)
	var rootCmd = &cobra.Command{
		Use:              "act [event name to run]",
		Short:            "Run Github actions locally by specifying the event name (e.g. `push`) or an action name directly.",
		Args:             cobra.MaximumNArgs(1),
		RunE:             newRunCommand(ctx, input),
		PersistentPreRun: setupLogging,
		Version:          version,
		SilenceUsage:     true,
	}
	rootCmd.Flags().BoolP("watch", "w", false, "watch the contents of the local repo and run when files change")
	rootCmd.Flags().BoolP("list", "l", false, "list workflows")
	rootCmd.Flags().StringP("job", "j", "", "run job")
	rootCmd.Flags().BoolVarP(&input.reuseContainers, "reuse", "r", false, "reuse action containers to maintain state")
	rootCmd.Flags().BoolVarP(&input.forcePull, "pull", "p", false, "pull docker image(s) if already present")
	rootCmd.Flags().StringVarP(&input.eventPath, "event", "e", "", "path to event JSON file")
	rootCmd.PersistentFlags().StringVarP(&input.workflowsPath, "workflows", "W", "./.github/workflows/", "path to workflow files")
	rootCmd.PersistentFlags().StringVarP(&input.workdir, "directory", "C", ".", "working directory")
	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "verbose output")
	rootCmd.PersistentFlags().BoolVarP(&input.logOutput, "output", "o", false, "log output from steps")
	rootCmd.PersistentFlags().BoolVarP(&input.dryrun, "dryrun", "n", false, "dryrun mode")
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}

}

func setupLogging(cmd *cobra.Command, args []string) {
	verbose, _ := cmd.Flags().GetBool("verbose")
	if verbose {
		log.SetLevel(log.DebugLevel)
	}
}

func newRunCommand(ctx context.Context, input *Input) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		planner, err := model.NewWorkflowPlanner(input.WorkflowsPath())
		if err != nil {
			return err
		}

		// Determine the event name
		var eventName string
		if len(args) > 0 {
			eventName = args[0]
		} else if events := planner.GetEvents(); len(events) > 0 {
			// set default event type to first event
			// this way user dont have to specify the event.
			log.Debugf("Using detected workflow event: %s", events[0])
			eventName = events[0]
		}

		// build the plan for this run
		var plan *model.Plan
		if jobID, err := cmd.Flags().GetString("job"); err != nil {
			return err
		} else if jobID != "" {
			log.Debugf("Planning job: %s", jobID)
			plan = planner.PlanJob(jobID)
		} else {
			log.Debugf("Planning event: %s", eventName)
			plan = planner.PlanEvent(eventName)
		}

		// check if we should just print the graph
		if list, err := cmd.Flags().GetBool("list"); err != nil {
			return err
		} else if list {
			return drawGraph(plan)
		}

		// run the plan
		config := &runner.Config{
			EventName:       eventName,
			EventPath:       input.EventPath(),
			ForcePull:       input.forcePull,
			ReuseContainers: input.reuseContainers,
			Workdir:         input.Workdir(),
			LogOutput:       input.logOutput,
		}
		runner, err := runner.New(config)
		if err != nil {
			return err
		}

		ctx = common.WithDryrun(ctx, input.dryrun)
		if watch, err := cmd.Flags().GetBool("watch"); err != nil {
			return err
		} else if watch {
			return watchAndRun(ctx, runner.NewPlanExecutor(plan))
		}

		return runner.NewPlanExecutor(plan)(ctx)
	}
}

func watchAndRun(ctx context.Context, fn common.Executor) error {
	recurse := true
	checkIntervalInSeconds := 2
	dir, err := os.Getwd()
	if err != nil {
		return err
	}

	var ignore *gitignore.GitIgnore
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); !os.IsNotExist(err) {
		ignore, _ = gitignore.CompileIgnoreFile(filepath.Join(dir, ".gitignore"))
	} else {
		ignore = &gitignore.GitIgnore{}
	}

	folderWatcher := fswatch.NewFolderWatcher(
		dir,
		recurse,
		ignore.MatchesPath,
		checkIntervalInSeconds,
	)

	folderWatcher.Start()

	go func() {
		for folderWatcher.IsRunning() {
			if err = fn(ctx); err != nil {
				break
			}
			log.Debugf("Watching %s for changes", dir)
			for changes := range folderWatcher.ChangeDetails() {
				log.Debugf("%s", changes.String())
				if err = fn(ctx); err != nil {
					break
				}
				log.Debugf("Watching %s for changes", dir)
			}
		}
	}()
	<-ctx.Done()
	folderWatcher.Stop()
	return err
}
