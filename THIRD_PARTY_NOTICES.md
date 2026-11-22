# Third-party notices

Storyboard Studio (AGPL-3.0-only OR commercial, see `NOTICE`) uses the components below. Each one
keeps its own license. The full license texts and NOTICE files are in **`THIRD_PARTY_LICENSES.txt`**,
which `scripts/third-party-licenses.sh` generates.

## 1. Compiled into the binary (Go modules)
Source: `go list -deps ./cmd/server`. All are permissive licenses, compatible with AGPL-3.0 and
with commercial licensing.

| Module | Version | License |
|---|---|---|
| Go standard library | go1.26.5 | BSD-3-Clause |
| github.com/anthropics/anthropic-sdk-go/v2 | v2.13.0 | Apache-2.0 |
| github.com/bahlo/generic-list-go | v0.2.0 | BSD-3-Clause |
| github.com/buger/jsonparser | v1.1.2 | MIT |
| github.com/go-logr/logr | v1.4.2 | Apache-2.0 |
| github.com/go-logr/stdr | v1.2.2 | Apache-2.0 |
| github.com/invopop/jsonschema | v0.14.0 | MIT |
| github.com/jackc/pgpassfile | v1.0.0 | MIT |
| github.com/jackc/pgservicefile | v0.0.0-20240606120523-5a60cdf6a761 | MIT |
| github.com/jackc/pgx/v5 | v5.13.0 | MIT |
| github.com/jackc/puddle/v2 | v2.2.3 | MIT |
| github.com/pb33f/ordered-map/v2 | v2.3.1 | Apache-2.0 |
| github.com/standard-webhooks/standard-webhooks/libraries | v0.0.1 | MIT |
| github.com/tidwall/gjson | v1.18.0 | MIT |
| github.com/tidwall/match | v1.1.1 | MIT |
| github.com/tidwall/pretty | v1.2.1 | MIT |
| github.com/tidwall/sjson | v1.2.5 | MIT |
| go.opentelemetry.io/auto/sdk | v1.1.0 | Apache-2.0 |
| go.opentelemetry.io/otel | v1.33.0 | Apache-2.0 |
| go.opentelemetry.io/otel/metric | v1.33.0 | Apache-2.0 |
| go.opentelemetry.io/otel/trace | v1.33.0 | Apache-2.0 |
| go.yaml.in/yaml/v4 | v4.0.0-rc.2 | Apache-2.0 (with NOTICE: Copyright 2011-2016 Canonical Ltd.) and MIT parts |
| golang.org/x/crypto | v0.40.0 | BSD-3-Clause |
| golang.org/x/sync | v0.22.0 | BSD-3-Clause |
| golang.org/x/sys | v0.35.0 | BSD-3-Clause |
| golang.org/x/text | v0.41.0 | BSD-3-Clause |

Test-only and tooling modules listed in `go.sum` are not shipped in the binary, so they are not
listed here.

## 2. Frontend
`web/static` contains only original code (vanilla HTML/CSS/JS). It uses no third-party libraries,
fonts or CDNs.

## 3. Runtime tools (not compiled in; run as separate programs)

| Component | Where | License | Notes |
|---|---|---|---|
| **FFmpeg** (`ffmpeg`, `ffprobe`) | Installed in the Docker runtime image from the Alpine package repository | LGPL-2.1-or-later, or GPL-2.0-or-later when built with GPL components | Storyboard Studio calls ffmpeg as an external process and does not link to it. If you **distribute the Docker image**, you also distribute FFmpeg: include its license and offer its source. Check the exact license with `docker run --rm --entrypoint apk <image> info --license ffmpeg`. Source: Alpine aports and https://ffmpeg.org. |
| Alpine Linux base image and packages (`ca-certificates`, `tzdata`, musl, busybox) | Docker runtime image | Various (MIT, GPL-2.0, MPL-2.0, public domain) | Distributed as part of the image. Package licenses: `apk info --license <package>`. |
| PostgreSQL (`postgres:18-alpine` / `postgres:18`) | `docker-compose.dokploy.yml` / `docker-compose.yml`, pulled by the user | PostgreSQL License | Not included in the Storyboard Studio image. |

## 4. External services (not software we distribute)
The app sends requests to the APIs of Anthropic, BytePlus ModelArk, OpenAI, Google (YouTube), Meta
(Instagram, Threads) and TikTok. Their terms of service, not this license, govern those requests.
Each operator needs its own account and agreement, and a data processing agreement where personal
data is involved.
