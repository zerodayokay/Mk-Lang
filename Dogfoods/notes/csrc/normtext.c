/*
 * normtext(x): x with ASCII letters lowered, runs of white space folded to one
 * space, and white space trimmed at both ends. NULL gives NULL. Bytes at or
 * above 0x80 pass through unchanged, so UTF-8 stays valid.
 *
 * Registered with sqlite3_auto_extension, so every connection opened after
 * notes_register_normtext() has it. Links against std.libc only.
 *
 * DEFECT DF-notes-05: the header is named by its path from the package root,
 * the one include directory mk is known to pass.
 */
#include "csrc/sqlite/sqlite3.h"
#include "normtext.h"

static int is_space(unsigned char c) {
    return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v';
}

static void normtext(sqlite3_context *ctx, int argc, sqlite3_value **argv) {
    const unsigned char *in;
    unsigned char *out;
    int n, i, j = 0, pending = 0;
    (void)argc;
    if (sqlite3_value_type(argv[0]) == SQLITE_NULL) {
        sqlite3_result_null(ctx);
        return;
    }
    in = sqlite3_value_text(argv[0]);
    n = sqlite3_value_bytes(argv[0]);
    if (in == 0) {
        sqlite3_result_error_nomem(ctx);
        return;
    }
    out = sqlite3_malloc(n + 1);
    if (out == 0) {
        sqlite3_result_error_nomem(ctx);
        return;
    }
    for (i = 0; i < n; i++) {
        unsigned char c = in[i];
        if (is_space(c)) {
            pending = j > 0;
            continue;
        }
        if (pending) {
            out[j++] = ' ';
            pending = 0;
        }
        if (c >= 'A' && c <= 'Z') {
            c = (unsigned char)(c - 'A' + 'a');
        }
        out[j++] = c;
    }
    sqlite3_result_text(ctx, (const char *)out, j, sqlite3_free);
}

static int init(sqlite3 *db, char **err, const sqlite3_api_routines *api) {
    (void)err;
    (void)api;
    return sqlite3_create_function(db, "normtext", 1, SQLITE_UTF8 | SQLITE_DETERMINISTIC, 0, normtext, 0, 0);
}

int notes_register_normtext(void) {
    return sqlite3_auto_extension((void (*)(void))init);
}

const char *notes_normtext_name(void) {
    return "normtext";
}
