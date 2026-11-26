System view archives, embedded in mk (internal/sysview/embed.go).

Each directory is a target; it holds one archive per system view
(<view>.mksv) and views.txt, the manifest of their inputs and counts.
They are generated, never edited:

    go run ./tools/sysview -sdk "C:\Program Files (x86)\Windows Kits\10"

The Windows views hold declarations generated from the Windows SDK's
headers (the version is in views.txt and in `mk sdk views`).
