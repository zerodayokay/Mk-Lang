# notes

A notes service: an HTTP JSON API over SQLite and its command-line client, in one program. It shows Mk linking C: the SQLite amalgamation through `std.db.sqlite`, and a C source of its own that adds an SQL function, with every call into C kept in one unsafe module.

## Running it

From the program's folder, with the bundled SQLite amalgamation:

```
cd Dogfoods/notes
mk run -- version
```

```text
notes 1.0
sqlite 3.54.1
normalizer normtext
```

| Command | Does |
|---|---|
| `notes serve --db FILE [--addr HOST:PORT]` | Serves the API (default 127.0.0.1:8080) until Ctrl-C, with a request log (`--log FILE`, else printed at exit) |
| `notes replay --db FILE` | Answers raw HTTP/1.1 requests read from standard input with the same handlers and writes the raw responses to standard output |
| `notes add\|get\|list\|search\|edit\|rm\|stats\|health\|version\|raw ...` | Sends one request to the server at `--addr` |
| `notes batch` | Runs one client command a line from standard input, `#` starting a comment |
| `notes load --clients N --requests M` | Posts N×M notes from N tasks at once and prints the totals and the store's stats |
| `notes import FILE` | Adds the notes of a JSON array in one transaction, or none |
| `notes export` | Prints every note as a JSON array |
| `notes backup --to FILE` | Copies every note of `--db` into `--to` |
| `notes --help` | The commands and options |

With `--embed --db FILE`, a client command also serves the database itself, in the same process, while the commands run. Exit codes: 0 success, 1 failure, 2 usage, 3 database locked, 4 the server refused a client request.

### The API

| Method and path | Does | Answers |
|---|---|---|
| `GET /health` | | 200 `{"status":"ok"}` |
| `GET /version` | sqlite's version and the normaliser's name | 200 |
| `GET /stats` | notes, distinct tags, words, words a note | 200 |
| `GET /notes?q=&tag=&limit=&offset=` | a page of notes in id order; `q` matches title or body through `normtext`, `tag` one tag | 200, 400 on a bad parameter |
| `POST /notes` | creates a note from `{"title", "body", "tags"?}` | 201 with `Location`; 400 malformed JSON with its byte offset; 422 wrong shape or limits; 409 title taken |
| `GET /notes/{id}` | | 200, 404 |
| `PUT /notes/{id}` | replaces a note | 200, 404, 409, 422 |
| `PATCH /notes/{id}` | changes the members given (`title`, `body`, `tags`) | 200, 404, 422 naming the member's index |
| `DELETE /notes/{id}` | | 200, 404 |

Any other method on a known path is 405 with `Allow`; an unknown path is 404; a request over `--limit` bytes is 413; a locked database is 503 with `Retry-After`. Errors carry `{"message", "offset"}`. A title is 1 to 200 bytes, a body up to 64 KiB, and a note has at most 16 tags of 1 to 32 bytes of `a-z0-9-`.

## Design

- `csrc/normtext.c`, linked against `std.libc`, registers the SQL function `normtext(x)` (ASCII lowered, white space folded and trimmed) on every connection through `sqlite3_auto_extension`. The store keys titles on `normtext(title)`, so `Shopping list` and `shopping   LIST` conflict (409), and search matches through it.
- Only `src/store/cext` calls C; `notes version` reads the function's name and the sqlite version back with `str.FromC`.
- The server runs one task per connection and one arena per request; routes are an `http.Route` table, and `http.Dispatch` answers 404 and 405 itself.
- The JSON types get their codecs from `gen json.Codec`; a PATCH body is read as a `json.Value` and checked member by member.
- Responses of 256 bytes or more are gzip-compressed when the client sends `Accept-Encoding: gzip`.
- One row per note, with its tags sorted and joined by spaces, so a note and its tags change in one statement.

## Limits

- `--log FILE` replaces the file: `std.fs` has no append mode.
- sqlite writes files through its own VFS, not through the `Io`, so disk faults under `std.sim` cannot reach the store.

## Tests

The cases in `cases/` run client sessions against an embedded server, replay raw requests (gzip, malformed, oversized), import, export and back up databases, and check usage errors and a locked database. Each client case serves its own database on a fixed loopback port of its own, a different one on Linux (`<case>.x86_64-linux.args`), so the two targets never share a port. Run them from the repository root with `go run ./tools/dogfood run -only notes`.

## Section 18 lines
The standard-library lines this program uses, as `doc/reference.md` lists them; `go run ./tools/dogfood cover` reads this block.

```
Arena.Sub(&self): OutOfMemory!Arena
str.FromC(p: *u8): str

// std.json. Value, Member and Error below are json.Value, json.Member and json.Error
*

// std.http. Request, Response and Error below are http.Request, http.Response and http.Error
*

// std.compress. Inflater and Error below are compress.Inflater and compress.Error
compress.Gzip(a: &Arena, data: []u8): OutOfMemory![]u8 from a
compress.Gunzip(a: &Arena, data: []u8, limit: int): compress.Error![]u8 from a

// std.cli. Error below is cli.Error
*

// std.log
*

// std.db.sqlite. Db, Stmt and Error below are sqlite.Db, sqlite.Stmt and sqlite.Error
*
```
