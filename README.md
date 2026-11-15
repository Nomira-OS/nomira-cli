# nomira-cli

Die Kommandozeile zu [nomira](https://github.com/Nomira-OS/nomira): ein
Befehl, der ein Recipe baut, prüft und das laufende System per bootc auf
das neue Image umstellt.

Die CLI kennt das Frontend nur als Container-Image. Gebaut wird über
`buildctl`; `lint` und `graph` rufen die Hilfsmodi des Frontend-Images per
podman auf. Das Frontend bleibt damit auch ohne diese CLI nutzbar.

## Befehle

```
nomira build    [flags]          Recipe zu einem OCI-Archiv bauen
nomira switch   [flags]          bauen, prüfen, dann bootc switch
nomira validate [flags] [image]  validation.d-Hooks und Boot-Test ausführen
nomira lint     [flags]          Recipe prüfen
nomira graph    [flags]          Pipeline als Mermaid-Flowchart ausgeben
```

Wichtige Flags (`nomira <befehl> -h` zeigt alle):

| Flag | Bedeutung |
|---|---|
| `-f recipe.yml` | Einstiegsdatei des Recipes |
| `--dry-run` | Befehle nur ausgeben, nichts ausführen |
| `--build-arg NAME=WERT` | einen in `settings.args` deklarierten Arg überschreiben |
| `--frontend IMAGE` | Frontend-Image; sonst gilt die `# syntax=`-Zeile des Recipes, sonst `ghcr.io/nomira-os/nomira:1` |
| `--name NAME` | Image-Name; sonst `settings.name`, sonst der Verzeichnisname |
| `--no-boot-test` | den bcvk-Boot-Test überspringen |
| `--apply` | nach `switch` sofort ins neue Image neu starten |

## Was `nomira switch` tut

1. buildkitd bereitstellen: Ist `BUILDKIT_HOST` gesetzt, wird dieser Daemon
   benutzt. Sonst startet die CLI einen podman-Container `nomira-buildkitd`
   und legt ihn bei Bedarf an.
2. Das Recipe mit `buildctl` bauen. Die Konfliktprüfung der Gruppen läuft
   dabei im Frontend. Ergebnis ist `.nomira/<name>.oci.tar` neben dem
   Recipe.
3. Das Archiv mit `skopeo` in den eigenen containers-storage laden.
4. Prüfen: alle `validation.d/*.sh` neben dem Recipe (sortiert, mit dem
   Image-Namen in `$IMG`), danach `bcvk ephemeral test-basic`, das das
   Image in einer Wegwerf-VM bootet.
5. Erst wenn alles bestanden ist: das Archiv per `sudo skopeo` in den
   containers-storage von root laden und `sudo bootc switch` aufrufen.

Bis Schritt 5 wird am laufenden System nichts verändert.
`nomira switch --dry-run` zeigt die genaue Befehlsfolge.

Beispiel für einen Hook, `validation.d/10-lint.sh`:

```sh
podman run --rm "$IMG" bootc container lint --fatal-warnings
```

## Voraussetzungen

`podman`, `buildctl` (Arch-Paket `buildkit`) und `skopeo`. Für `switch`
zusätzlich `bootc` und `sudo`. `bcvk` ist optional: Fehlt es, wird der
Boot-Test mit einem Hinweis übersprungen.

Umgebungsvariablen: `BUILDKIT_HOST`, `NOMIRA_BUILDKIT_IMAGE` (Image für den
verwalteten buildkitd-Container), `NOMIRA_SUDO` (Befehl für root-Rechte).

## Bauen

```sh
go build -o nomira .
go test ./...
```

Die CLI hat keine Abhängigkeiten außer der Go-Standardbibliothek.

## Stand

Die Befehlsfolge ist über Tests im Dry-Run abgesichert. Ein echter Lauf
gegen buildkitd, skopeo und bootc steht noch aus.
