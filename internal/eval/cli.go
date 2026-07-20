package eval

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
)

func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("kontext-eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var (
		suitePath    string
		namespace    string
		kubeconfig   string
		runtimeImage string
		recordPath   string
		summaryPath  string
		judgeCommand string
		keepRuns     bool
	)
	flags.StringVar(&suitePath, "suite", "", "path to an EvalSuite YAML file (required)")
	flags.StringVar(&namespace, "namespace", "", "namespace override for created AgentRuns")
	flags.StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (defaults to standard loading rules)")
	flags.StringVar(&runtimeImage, "runtime-image", os.Getenv("KONTEXT_EVAL_RUNTIME_IMAGE"), "runtime image override (or KONTEXT_EVAL_RUNTIME_IMAGE)")
	flags.StringVar(&recordPath, "records", "", "JSONL record output path")
	flags.StringVar(&summaryPath, "summary", "", "JSON summary output path")
	flags.StringVar(&judgeCommand, "judge-command", "", "optional external judge command; failures fail the evaluation")
	flags.BoolVar(&keepRuns, "keep-runs", false, "keep AgentRuns created by this invocation")
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: kontext-eval --suite FILE [options]\n\n")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if suitePath == "" {
		fmt.Fprintln(stderr, "--suite is required")
		flags.Usage()
		return 2
	}
	suite, err := LoadSuite(suitePath, runtimeImage)
	if err != nil {
		fmt.Fprintf(stderr, "load suite: %v\n", err)
		return 2
	}
	if namespace != "" {
		suite.Spec.Defaults.Namespace = namespace
	}
	startedAt := time.Now().UTC()
	if recordPath == "" || summaryPath == "" {
		base := filepath.Join(
			"eval-results",
			NameForCase(suite.Metadata.Name, startedAt.Format("20060102t150405z"), ""),
		)
		if recordPath == "" {
			recordPath = base + ".jsonl"
		}
		if summaryPath == "" {
			summaryPath = base + ".summary.json"
		}
	}

	config, err := loadRESTConfig(kubeconfig)
	if err != nil {
		fmt.Fprintf(stderr, "load kubeconfig: %v\n", err)
		return 2
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		fmt.Fprintf(stderr, "configure core scheme: %v\n", err)
		return 2
	}
	if err := kontextv1alpha1.AddToScheme(scheme); err != nil {
		fmt.Fprintf(stderr, "configure Kontext scheme: %v\n", err)
		return 2
	}
	controllerClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintf(stderr, "create Kubernetes client: %v\n", err)
		return 2
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Fprintf(stderr, "create Kubernetes log client: %v\n", err)
		return 2
	}
	var judge Judge
	if strings.TrimSpace(judgeCommand) != "" {
		judge = CommandJudge{Command: judgeCommand}
	}
	runner := Runner{
		Client: controllerClient,
		Logs:   NewKubernetesLogFetcher(clientset),
		Options: RunnerOptions{
			Namespace: namespace,
			KeepRuns:  keepRuns,
			Judge:     judge,
		},
	}
	records := runner.RunSuite(ctx, suite)
	assertions := EvaluateSuiteAssertions(suite.Spec.Assertions, records)
	completedAt := time.Now().UTC()
	summary := BuildSummary(
		suite.Metadata.Name,
		len(suite.Spec.Cases),
		startedAt,
		completedAt,
		records,
		assertions,
		recordPath,
	)
	if err := WriteOutputs(recordPath, summaryPath, records, summary); err != nil {
		fmt.Fprintf(stderr, "write evaluation outputs: %v\n", err)
		return 1
	}
	fmt.Fprintf(
		stdout,
		"suite=%s expected=%d total=%d passed=%d failed=%d collectionErrors=%d assertionFailures=%d records=%s summary=%s\n",
		summary.Suite,
		summary.ExpectedTotal,
		summary.Total,
		summary.Passed,
		summary.Failed,
		summary.CollectionErrorCount,
		summary.AssertionFailures,
		recordPath,
		summaryPath,
	)
	if !summary.Pass {
		return 1
	}
	return 0
}

func loadRESTConfig(path string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path != "" {
		rules.ExplicitPath = path
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules,
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
}
