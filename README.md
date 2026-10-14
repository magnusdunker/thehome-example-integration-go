# Eksempel-tæller til TheHome (Go)

En lille ekstern integration til [TheHome](https://github.com/magnusdunker/thehome), skrevet i Go. Den gør det samme som [Rust-eksemplet](https://github.com/magnusdunker/thehome-example-integration) og er et udgangspunkt for at skrive sin egen integration i Go.

Hver opsætning giver en enhed med:

- en sensor, **Antal**, der tæller én op med et fast interval
- en kontakt, **Tæller**, der sætter tællingen på pause
- en knap, **Nulstil**, der sætter tallet til 0 og sender hændelsen `example_counter_go_reset`

Desuden har den tjenesten `example_counter_go.set`, der sætter en tæller til en bestemt værdi, og et options-forløb, hvor intervallet kan ændres. Tallet gemmes i integrationens datamappe, så det overlever en genstart.

## Installation

I TheHome: **Indstillinger → Integrationer → Fra GitHub**. Indsæt

```
https://github.com/magnusdunker/thehome-example-integration-go
```

og tryk **Installér**. Tilføj derefter **Eksempel-tæller (Go)** under **Tilføj integration**.

## Sådan virker det

En ekstern integration er et selvstændigt program. TheHome starter det og taler med det over JSON-RPC på stdin og stdout. Protokollen er beskrevet i TheHomes [docs/custom-integrations.md](https://github.com/magnusdunker/thehome/blob/main/docs/custom-integrations.md).

Repoet indeholder:

- `thehome.json`: domæne, navn og protokolversion
- `thehomeplugin/`: SDK'en, der klarer protokollen. Den bruger kun standardbiblioteket.
- `cmd/example_counter_go/`: selve integrationen
- `.github/workflows/release.yml`: bygger en binær til hver platform og lægger dem på en release

## Lav din egen

1. Kopiér repoet og ret `domain` og `name` i `thehome.json`. Domænet må kun indeholde små bogstaver, tal og `_` og må ikke være en indbygget integrations domæne.
2. Ret modulnavnet i `go.mod` og `DOMAIN` og `MAIN` øverst i `release.yml`.
3. Lav en type, der indlejrer `thehomeplugin.Base` og implementerer `SetupEntry`. Overskriv de andre metoder efter behov, og kald `thehomeplugin.Run` fra `main`.
4. Push et tag, f.eks. `git tag v0.1.0 && git push --tags`. Workflowet bygger til Windows, Linux (x86_64 og arm64) og macOS og opretter releasen.

Go krydskompilerer statisk med `CGO_ENABLED=0`, så én Linux-runner bygger alle platforme, og binærerne kører i TheHomes Docker-image uden ekstra biblioteker. Linux-binærerne hedder `...-unknown-linux-musl`, fordi TheHome foretrækker statiske binærer under det navn.

TheHome henter altid den nyeste release. Binærerne skal hedde `<domæne>-<target>`, f.eks. `example_counter_go-aarch64-unknown-linux-musl`.

Kør testene med `go test ./...`. Programmet venter på beskeder fra TheHome på stdin, så det gør ingenting, når det startes alene.

## Licens

MIT
