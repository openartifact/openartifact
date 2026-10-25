// SPDX-FileCopyrightText: 2023 Google LLC
// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: Apache-2.0

// Replacement for $GOPATH/gosigfuzz/gosigfuzz.c of the base-builder-go image.
// build.sh recompiles it over the image's gosigfuzz.o, which
// $LIB_FUZZING_ENGINE links into every Go fuzz binary. It is the upstream file
// plus one change: LLVMFuzzerInitialize first waits for the Go runtime.
//
// A c-archive Go program initialises its runtime on a background thread while
// main() already runs libFuzzer. The runtime registers its coverage counters
// during that initialisation; libFuzzer builds its Fuzzer object, which walks
// the registered counter regions, right after LLVMFuzzerInitialize. Without the
// wait that walk races the registration and sometimes dereferences a null
// region (SEGV at 0x10 in TracePC::ClearInlineCounters, zero executed units,
// a random target on a random run). See google/oss-fuzz#10907.

#include <signal.h>
#include <stdint.h>
#include <stdlib.h>

extern void _cgo_wait_runtime_init_done(void) __attribute__((weak));

static void fixSignalHandler(int signum) {
    struct sigaction new_action;
    struct sigaction old_action;
    sigemptyset (&new_action.sa_mask);
    sigaction (signum, NULL, &old_action);
    new_action.sa_flags = old_action.sa_flags | SA_ONSTACK;
    new_action.sa_sigaction = old_action.sa_sigaction;
    new_action.sa_handler = old_action.sa_handler;
    sigaction (signum, &new_action, NULL);
}

static void FixStackSignalHandler() {
    fixSignalHandler(SIGSEGV);
    fixSignalHandler(SIGABRT);
    fixSignalHandler(SIGALRM);
    fixSignalHandler(SIGINT);
    fixSignalHandler(SIGTERM);
    fixSignalHandler(SIGBUS);
    fixSignalHandler(SIGFPE);
    fixSignalHandler(SIGXFSZ);
    fixSignalHandler(SIGUSR1);
    fixSignalHandler(SIGUSR2);
}

int LLVMFuzzerInitialize(int *argc, char ***argv) {
    if (_cgo_wait_runtime_init_done) {
        _cgo_wait_runtime_init_done();
    }

    FixStackSignalHandler();
    return 0;
}
