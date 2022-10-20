package bazel

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"

	"code-intelligence.com/cifuzz/internal/build"
	"code-intelligence.com/cifuzz/internal/cmdutils"
	"code-intelligence.com/cifuzz/pkg/log"
	"code-intelligence.com/cifuzz/pkg/runfiles"
	"code-intelligence.com/cifuzz/util/archiveutil"
	"code-intelligence.com/cifuzz/util/envutil"
	"code-intelligence.com/cifuzz/util/fileutil"
)

type BuilderOptions struct {
	ProjectDir string
	Engine     string
	NumJobs    uint
	Stdout     io.Writer
	Stderr     io.Writer
	TempDir    string
	Verbose    bool
}

func (opts *BuilderOptions) Validate() error {
	// Check that the project dir is set
	if opts.ProjectDir == "" {
		return errors.New("ProjectDir is not set")
	}

	// Check that the project dir exists and can be accessed
	_, err := os.Stat(opts.ProjectDir)
	if err != nil {
		return errors.WithStack(err)
	}

	// Check that the TempDir is set. This is not set by the user, so
	// we panic if it's not set
	if opts.TempDir == "" {
		panic("TempDir is not set")
	}

	if opts.Engine != "libfuzzer" {
		panic(fmt.Sprintf("Invalid engine %q", opts.Engine))
	}

	return nil
}

type Builder struct {
	*BuilderOptions
}

func NewBuilder(opts *BuilderOptions) (*Builder, error) {
	err := opts.Validate()
	if err != nil {
		return nil, err
	}

	b := &Builder{BuilderOptions: opts}
	return b, nil
}

// BuildForRun builds the specified fuzz tests with bazel. It expects
// labels of targets of the cc_fuzz_test rule provided by rules_fuzzing:
// https://github.com/bazelbuild/rules_fuzzing/blob/master/docs/cc-fuzzing-rules.md#cc_fuzz_test
//
// TODO: Unfortunately, the cc_fuzz_test rule currently doesn't
// support combining sanitizers, so we can't build with both ASan
// and UBSan. Therefore, we only build with ASan and plan to upstream
// support for combining sanitizers.
func (b *Builder) BuildForRun(fuzzTests []string) ([]*build.Result, error) {
	var err error

	var binLabels []string
	for i := range fuzzTests {
		// The cc_fuzz_test rule defines multiple bazel targets: If the
		// name is "foo", it defines the targets "foo", "foo_bin", and
		// others. We need to run the "foo_bin" target but want to
		// allow users to specify either "foo" or "foo_bin", so we check
		// if the fuzz test name  appended with "_bin" is a valid target
		// and use that in that case
		cmd := exec.Command("bazel", "query", fuzzTests[i]+"_bin")
		err := cmd.Run()
		if err == nil {
			binLabels = append(binLabels, fuzzTests[i]+"_bin")
		} else {
			fuzzTests[i] = strings.TrimSuffix(fuzzTests[i], "_bin")
			binLabels = append(binLabels, fuzzTests[i]+"_bin")
		}
	}

	// The BuildDir field of the build results is expected to be a
	// parent directory of all the artifacts, so that a single minijail
	// binding allows access to all artifacts in the sandbox.
	// When building via bazel, the "output_base" directory contains
	// all artifacts, so we use that as the BuildDir.
	cmd := exec.Command("bazel", "info", "output_base")
	out, err := cmd.Output()
	if err != nil {
		// It's expected that bazel might fail due to user configuration,
		// so we print the error without the stack trace.
		err = cmdutils.WrapExecError(errors.WithStack(err), cmd)
		log.Error(err)
		return nil, cmdutils.ErrSilent
	}
	buildDir := strings.TrimSpace(string(out))
	fuzzScript := filepath.Join(b.TempDir, "fuzz.sh")

	// To avoid part of the loading and/or analysis phase to rerun, we
	// use the same flags for all bazel commands (except for those which
	// are not supported by all bazel commands we use).
	sharedFlags := []string{
		"--repo_env=CC",
		"--repo_env=CXX",
		// Don't use the LLVM from Xcode
		"--repo_env=BAZEL_USE_CPP_ONLY_TOOLCHAIN=1",
	}
	if b.NumJobs != 0 {
		sharedFlags = append(sharedFlags, "--jobs", fmt.Sprint(b.NumJobs))
	}

	// Flags which should only be used for bazel run because they are
	// not supported by the other bazel commands we use
	runFlags := []string{
		// Build with debug symbols
		"-c", "opt", "--copt", "-g",
		// Disable source fortification, which is currently not supported
		// in combination with ASan, see https://github.com/google/sanitizers/issues/247
		"--copt", "-U_FORTIFY_SOURCE",
		// Build with libFuzzer
		"--@rules_fuzzing//fuzzing:cc_engine=@rules_fuzzing//fuzzing/engines:libfuzzer",
		"--@rules_fuzzing//fuzzing:cc_engine_instrumentation=libfuzzer",
		// Build with ASan instrumentation
		"--@rules_fuzzing//fuzzing:cc_engine_sanitizer=asan",
		"--verbose_failures",
		"--script_path=" + fuzzScript,
	}
	args := []string{"run"}
	args = append(args, sharedFlags...)
	args = append(args, runFlags...)
	args = append(args, binLabels...)

	cmd = exec.Command("bazel", args...)
	// Redirect the build command's stdout to stderr to only have
	// reports printed to stdout
	cmd.Stdout = b.Stderr
	cmd.Stderr = b.Stderr
	cmd.Env, err = build.CommonBuildEnv()
	if err != nil {
		return nil, err
	}
	log.Debugf("Command: %s", cmd.String())
	err = cmd.Run()
	if err != nil {
		// It's expected that bazel might fail due to user configuration,
		// so we print the error without the stack trace.
		err = cmdutils.WrapExecError(errors.WithStack(err), cmd)
		log.Error(err)
		return nil, cmdutils.ErrSilent
	}

	// Assemble the build results
	var results []*build.Result

	for _, fuzzTest := range fuzzTests {
		// Turn the fuzz test label into a valid path
		path, err := pathFromLabel(fuzzTest, sharedFlags)
		if err != nil {
			return nil, err
		}
		seedCorpus := filepath.Join(b.ProjectDir, path+"_inputs")
		generatedCorpus := filepath.Join(b.ProjectDir, ".cifuzz-corpus", path)

		result := &build.Result{
			Name:            path,
			Executable:      fuzzScript,
			GeneratedCorpus: generatedCorpus,
			SeedCorpus:      seedCorpus,
			BuildDir:        buildDir,
			Engine:          b.Engine,
			Sanitizers:      []string{"address"},
		}
		results = append(results, result)
	}

	return results, nil
}

func (b *Builder) BuildForBundle(engine string, sanitizers []string, fuzzTests []string) ([]*build.Result, error) {
	var err error

	env, err := build.CommonBuildEnv()
	if err != nil {
		return nil, err
	}

	switch engine {
	case "libfuzzer":
		env, err = b.setLibFuzzerEnv(env)
		if err != nil {
			return nil, err
		}
	default:
		panic(fmt.Sprintf("Invalid engine %q", engine))
	}

	// To avoid part of the loading and/or analysis phase to rerun, we
	// use the same flags for all bazel commands (except for those which
	// are not supported by all bazel commands we use).
	sharedFlags := []string{
		"--repo_env=CC",
		"--repo_env=CXX",
		"--repo_env=FUZZING_CFLAGS",
		"--repo_env=FUZZING_CXXFLAGS",
		// Don't use the LLVM from Xcode
		"--repo_env=BAZEL_USE_CPP_ONLY_TOOLCHAIN=1",
	}
	if b.NumJobs != 0 {
		sharedFlags = append(sharedFlags, "--jobs", fmt.Sprint(b.NumJobs))
	}

	// Flags which should only be used for bazel build
	buildFlags := []string{
		"--@rules_fuzzing//fuzzing:cc_engine=@rules_fuzzing_oss_fuzz//:oss_fuzz_engine",
		"--@rules_fuzzing//fuzzing:cc_engine_instrumentation=oss-fuzz",
		"--verbose_failures",
	}

	// Add sanitizer-specific flags
	if len(sanitizers) == 1 && sanitizers[0] == "coverage" {
		llvmCov, err := runfiles.Finder.LLVMCovPath()
		if err != nil {
			return nil, err
		}
		llvmProfData, err := runfiles.Finder.LLVMProfDataPath()
		if err != nil {
			return nil, err
		}
		sharedFlags = append(sharedFlags,
			"--repo_env=BAZEL_USE_LLVM_NATIVE_COVERAGE=1",
			"--repo_env=GCOV="+llvmProfData,
			"--repo_env=BAZEL_LLVM_COV="+llvmCov,
		)
		buildFlags = append(buildFlags,
			"--@rules_fuzzing//fuzzing:cc_engine_instrumentation=none",
			"--@rules_fuzzing//fuzzing:cc_engine_sanitizer=none",
			"--instrument_test_targets",
			"--experimental_use_llvm_covmap",
			"--experimental_generate_llvm_lcov",
			"--collect_code_coverage",
		)
	} else {
		for _, sanitizer := range sanitizers {
			switch sanitizer {
			case "address", "undefined":
				// ASan and UBSan are already enabled above by the call
				// to b.setLibFuzzerEnv, which sets the respective flags
				// via the FUZZING_CFLAGS environment variable.
			default:
				panic(fmt.Sprintf("Invalid sanitizer for engine %q: %q", b.Engine, sanitizer))
			}
		}
	}

	args := []string{"build"}
	args = append(args, sharedFlags...)
	args = append(args, buildFlags...)

	// We have to build the "*_oss_fuzz" target defined by the
	// cc_fuzz_test rule
	var labels []string
	for _, fuzzTestLabel := range fuzzTests {
		labels = append(labels, fuzzTestLabel+"_oss_fuzz")
	}
	args = append(args, labels...)

	cmd := exec.Command("bazel", args...)
	// Redirect the build command's stdout to stderr to only have
	// reports printed to stdout
	cmd.Stdout = b.Stderr
	cmd.Stderr = b.Stderr
	cmd.Env = env
	log.Debugf("Command: %s", cmd.String())
	err = cmd.Run()
	if err != nil {
		// It's expected that bazel might fail due to user configuration,
		// so we print the error without the stack trace.
		err = cmdutils.WrapExecError(errors.WithStack(err), cmd)
		log.Error(err)
		return nil, cmdutils.ErrSilent
	}

	// Assemble the build results
	var results []*build.Result

	for _, fuzzTest := range fuzzTests {
		// Get the path to the archive created by the build
		args := []string{"cquery", "--output=starlark", "--starlark:expr=target.files.to_list()[0].path"}
		args = append(args, sharedFlags...)
		args = append(args, fuzzTest+"_oss_fuzz")
		cmd = exec.Command("bazel", args...)
		out, err := cmd.Output()
		if err != nil {
			// It's expected that bazel might fail due to user configuration,
			// so we print the error without the stack trace.
			err = cmdutils.WrapExecError(errors.WithStack(err), cmd)
			log.Error(err)
			return nil, cmdutils.ErrSilent
		}
		ossFuzzArchive := strings.TrimSpace(string(out))

		// Extract the archive
		extractedDir, err := os.MkdirTemp(b.TempDir, "extracted-")
		if err != nil {
			return nil, errors.WithStack(err)
		}
		err = archiveutil.UntarFile(ossFuzzArchive, extractedDir)
		if err != nil {
			return nil, err
		}

		path, err := pathFromLabel(fuzzTest, sharedFlags)
		if err != nil {
			return nil, err
		}
		executable := filepath.Join(extractedDir, filepath.Base(path))

		// Extract the seed corpus
		ossFuzzSeedCorpus := executable + "_seed_corpus.zip"
		extractedCorpus := executable + "_seed_corpus"
		err = archiveutil.Unzip(ossFuzzSeedCorpus, extractedCorpus)
		if err != nil {
			return nil, err
		}

		// Find the runtime dependencies. The bundler will include them
		// in the bundle because below we set the BuildDir field of the
		// build.Result to extractedCorpus, which contains all the
		// runtime dependencies, causing the bundler to treat them all
		// as created by the build and therefore including them in the
		// bundle.
		var runtimeDeps []string
		runfilesDir := executable + ".runfiles"
		exists, err := fileutil.Exists(runfilesDir)
		if err != nil {
			return nil, err
		}
		if exists {
			err = filepath.WalkDir(runfilesDir, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return errors.WithStack(err)
				}
				if d.IsDir() {
					return nil
				}
				runtimeDeps = append(runtimeDeps, path)
				return nil
			})
			if err != nil {
				return nil, err
			}
		}

		result := &build.Result{
			Name:       path,
			Executable: executable,
			SeedCorpus: extractedCorpus,
			BuildDir:   extractedDir,
			// Bazel builds files with PWD=/proc/self/cwd
			ProjectDir:  "/proc/self/cwd",
			Engine:      b.Engine,
			Sanitizers:  sanitizers,
			RuntimeDeps: runtimeDeps,
		}
		results = append(results, result)
	}

	return results, nil
}

func (b *Builder) setLibFuzzerEnv(env []string) ([]string, error) {
	var err error

	// Set FUZZING_CFLAGS and FUZZING_CXXFLAGS.
	cflags := build.LibFuzzerCFlags()

	// This is only required when linking but rules_fuzzing doesn't
	// support specifying linker flags, so we pass it as a compiler flag
	// instead
	cflags = append(cflags, "-fsanitize=fuzzer")

	env, err = envutil.Setenv(env, "FUZZING_CFLAGS", strings.Join(cflags, " "))
	if err != nil {
		return nil, err
	}
	env, err = envutil.Setenv(env, "FUZZING_CXXFLAGS", strings.Join(cflags, " "))
	if err != nil {
		return nil, err
	}
	return env, nil
}

func pathFromLabel(label string, flags []string) (string, error) {
	// Get a canonical form of label via `bazel query`
	args := append([]string{"query"}, flags...)
	args = append(args, label)
	cmd := exec.Command("bazel", args...)
	log.Debugf("Command: %s", cmd.String())
	out, err := cmd.Output()
	if err != nil {
		// It's expected that bazel might fail due to user configuration,
		// so we print the error without the stack trace.
		err = cmdutils.WrapExecError(errors.WithStack(err), cmd)
		log.Error(err)
		return "", cmdutils.ErrSilent
	}
	canonicalLabel := strings.TrimSpace(string(out))

	// Transform the label into a valid path below the directory which
	// contains the BUILD file, which:
	// * Doesn't contain any leading '//'
	// * Has any ':' and '/' replaced with the path separator (':' is
	//   not allowed in filenames on Windows)
	res := strings.TrimPrefix(canonicalLabel, "//")
	res = strings.ReplaceAll(res, ":", "/")
	res = strings.ReplaceAll(res, "/", string(filepath.Separator))

	return res, nil
}
