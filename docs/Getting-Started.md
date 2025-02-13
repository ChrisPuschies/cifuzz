# Getting started

**cifuzz** commands will interactively guide you through the needed
options and show next steps. You can find a complete list of the available
commands with all supported options and parameters by calling `cifuzz command
--help` or read about them in the
[wiki](https://github.com/CodeIntelligenceTesting/cifuzz/wiki/cifuzz).

1. To initialize your project with cifuzz just execute `cifuzz init` in the
   root directory of your project. This will create a file named `cifuzz.yaml`
   containing the needed configuration and print out any necessary steps to
   set up your project.

2. The next step is to create a fuzz test. Execute `cifuzz create` and follow
   the instructions given by the command. This will create a stub for your fuzz
   test, for example `my_fuzz_test_1.cpp` and tell you how to integrate it into
   your project. You will find more detailed information in our
   [Tutorial](How-To-Write-A-Fuzz-Test.md).

3. Edit the fuzz test stub so it actually calls the function you want to test
   with the input generated by the fuzzer. To learn more about writing fuzz
   tests you can take a look at our [Tutorial](How-To-Write-A-Fuzz-Test.md) or
   one of the [example projects](../examples).

4. Start fuzzing by executing `cifuzz run my_fuzz_test_1`. **cifuzz** now builds
   the fuzz test and starts a fuzzing run.

## Generate coverage report

Once you executed a fuzz test, you can generate a coverage report which shows
the line coverage of the fuzzed code:

    cifuzz coverage my_fuzz_test_1

See [coverage IDE integrations](Coverage-ide-integrations.md) for instructions
on how to generate and visualize coverage reports right from your IDE.

## Regression testing

If you are interested in running your fuzz tests as regression tests to maintain 
a fast and responsive development cycle, you can check out our 
[regression testing](Regression-Testing.md) guide.

## Sandboxing

On Linux, **cifuzz** runs fuzz tests in a sandbox by default to prevent them
from accidentally harming the system, for example by deleting files or killing
processes. [Minijail](https://google.github.io/minijail/minijail0.1.html) is
used for this purpose.

If you experience problems when running fuzz tests via **cifuzz** and you don't
expect your fuzz tests to do any harm to the system (or you're already running
**cifuzz** in a container), you might want to disable the sandbox via the
`--use-sandbox=false` flag or the [`use-sandbox: false` config file
setting](docs/Configuration.md#use-sandbox).

If your fuzz test needs to access some files or directories which are not
accessible in the sandbox, you can add bindings for those via the
`CIFUZZ_MINIJAIL_BINDINGS` environment variable. The bindings must be separated
by colon and be specified in the same format that is supported by the
`--bind-mount` flag of [`minijail0`](https://google.github.io/minijail/minijail0.1.html):

`CIFUZZ_MINIJAIL_BINDINGS=<src>[,[dest][,<writeable>]]`, where `<src>` must be an absolute path
and `<writeable>` is either `0` or `1`. For example:
```
CIFUZZ_MINIJAIL_BINDINGS=/tmp/foo,/tmp/foo,1:/home/user/foo,/home/user/foo,1
```

## Intro to cifuzz (live stream)

Check out [@jochil](https://github.com/jochil)'s live session for
[a walkthrough](https://www.code-intelligence.com/webinar/uncovering-hidden-bugs-and-vulnerabilities)
of how to get started with cifuzz. The event is freely accessible on YouTube
and Linkedin.

Also, watch [Going Beyond Unit Testing | How to Uncover Blind Spots in your
Java Code with Fuzzing](https://www.youtube.com/watch?v=8yECb-p3cQI) on
YouTube.
