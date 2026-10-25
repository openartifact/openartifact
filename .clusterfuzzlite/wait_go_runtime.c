// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Linked into every Go fuzz binary by build.sh.
//
// A c-archive Go program initialises its runtime on a background thread while
// main() already runs libFuzzer. The runtime registers its coverage counters
// during that initialisation; libFuzzer builds its Fuzzer object, which walks
// the registered counter regions, right after LLVMFuzzerInitialize. Without a
// wait that walk races the registration and sometimes dereferences a null
// region (SEGV at 0x10 in TracePC::ClearInlineCounters, zero executed units,
// a random target on a random run). See google/oss-fuzz#10907.
//
// libFuzzer declares LLVMFuzzerInitialize weak; defining it here makes the
// driver call it before it constructs the Fuzzer.

extern void _cgo_wait_runtime_init_done(void) __attribute__((weak));

int LLVMFuzzerInitialize(int *argc, char ***argv) {
    (void)argc;
    (void)argv;

    if (_cgo_wait_runtime_init_done) {
        _cgo_wait_runtime_init_done();
    }

    return 0;
}
