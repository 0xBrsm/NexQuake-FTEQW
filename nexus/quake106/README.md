# Shareware Extractor

A standalone Go package that extracts `pak0.pak` directly from the original id Software Quake 1.06 shareware distribution (`quake106.zip`). On first run, if no game data exists, Nexus downloads `quake106.zip` and uses this package to extract a verified `pak0.pak` file into `id1/`.

## How It Works

The original shareware archive uses a deprecated, multi-part LHA-compressed installer format from 1996. This package implements LZH (LH5) decompression from scratch in pure Go (no cgo, no external binaries, no shell calls) to decompress the `resource.1` segment and extract the PAK file and license text.

Every step is SHA256-verified: the zip file itself, the `resource.1` entry, and the extracted `pak0.pak`. If any hash does not match the known-good value, extraction fails. This verification process serves two purposes:

1. **Correctness.** The engine expects specific file layouts. A corrupted or modified PAK causes subtle gameplay issues or crashes.
2. **License compliance.** id Software's shareware license permits redistribution of the original, unmodified archive only. The extraction pipeline allows NexQuake to get up and running with minimal user effort while still complying with that license.

## LZH Lineage

The LZH decoding logic is derived from [koron-go/lha](https://github.com/koron-go/lha) (MIT licensed), heavily modified and optimized specifically for Quake 1.06 resource extraction. See [ATTRIBUTIONS.md](../../ATTRIBUTIONS.md) for full provenance.

## Shareware Sources

- https://github.com/0xBrsm/QuakeAssets/blob/main/q1/quake106.zip
- https://github.com/Jason2Brownlee/QuakeOfficialArchive/blob/main/bin/quake106.zip
- https://www.gamers.org/pub/idgames/idstuff/quake/quake106.zip
