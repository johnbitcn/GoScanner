[English](README.md) | [简体中文](README-CN.md)

# GoScanner

## Introduction

**Use network eSCL or WSD scanners on macOS without a vendor-specific scanning driver.**

GoScanner communicates with devices through eSCL or WSD scan services. It provides an interactive terminal interface and a JSON mode for Agents. It can read device capabilities, scan pages, save multipage PDFs, and optionally run AI deskew and cropping locally.

The project does not restrict device brands, but a device must expose an eSCL or WSD scan service. **Support for WSD printing does not imply support for WSD scanning.** USB-only devices and proprietary scanning protocols are outside the current scope.

<p align="center">
  <img
    src="https://github.com/user-attachments/assets/9178da8a-b5ae-407a-a454-afa0f5b3f633"
    alt="Animated demo"
    width="800"
  />
</p>

## Installation and first scan

### The simplest installation method

Requires an **Apple Silicon Mac, macOS 14 or later, and Node.js 22 or later**:

```bash
npm install -g @codingbone/goscanner
```

On the first launch, select a scanner from the device list, or choose “Add manually” and enter its IP address. The program discovers the actual service address, validates it without scanning, and then saves the default device. When it shows “Ready,” press Enter to start scanning. You do not need to enter a scan port, path, or protocol.

The registered preferred protocol is used by default. eSCL is preferred when both protocols are first confirmed. Press `p` in the device list to change and save the preferred protocol. Connection failures report their cause; they do not trigger automatic protocol switching or rescanning.

## Command reference

### View the version

```bash
goscanner --version
goscanner -v
```

Both commands print the version number and exit. The version is embedded from `package.json` at build time; no source files, user configuration, scanner connection, or AI models are needed at runtime. Agent callers can use `goscanner --agent --version` to receive a JSON object with `status` and `version`.

### View help

```bash
goscanner --help
goscanner scan --help
goscanner info --help
goscanner discover --help
```

Top-level help shows navigation and common examples. Subcommand help groups options by purpose. Descriptions wrap and align to the terminal width; narrow terminals place descriptions below options. Human help reads only the language setting from configuration. It does not query devices, clear the screen, or use a pager, and it remains available when configuration is damaged.

In an interactive terminal, normal commands use an inline TUI, not a full-screen interface. If standard input or output is not a terminal, or `TERM=dumb`, the program uses plain-text mode. **Redirecting output does not automatically enable Agent mode.** The TUI provides foreground palettes for dark and light backgrounds without setting a background color. Priority is `--theme` > configuration `theme` > terminal background detection > dark fallback. Detection never blocks startup or prompts for a choice. Automatic mode follows terminal changes when possible and polls once per second after a successful detection. `NO_COLOR=1` disables TUI colors and help styling; the TUI retains the control sequences needed for interaction.

### Interface language

The human TUI, help, scan prompts, and error explanations support English and Simplified Chinese:

```bash
goscanner --lang zh-CN
goscanner --lang en scan --help
goscanner scan --lang zh-CN -o 合同.pdf
```

Language priority is **explicit `--lang` > configuration `language` > automatic system-language detection**. Both the option and configuration accept `auto`, `en`, and `zh-CN`. An omitted configuration field defaults to `auto`. Automatic detection uses the first nonempty value of `LC_ALL`, `LC_MESSAGES`, and `LANG`, in that order. Missing or unsupported languages fall back to English.

Add `"language": "zh-CN"` to your existing `~/.config/goscanner/config.json` to keep the Chinese interface. You can also choose and save a language in TUI settings. `--lang en` overrides this run only; it does not save a language preference. Explicit `--lang auto` bypasses a fixed language in configuration and detects the system language directly. Place language options before `--help` when requesting help.

Project-generated Agent messages always use English, regardless of `--lang`, configuration `language`, or the system language. Device names, paths, user input, and original external diagnostics are not forcibly translated. See [Human and Agent interfaces (Chinese)](docs/interfaces.md) for the complete contract.

### `goscanner`: interactive scanning

With no arguments, the program opens the scan interface in an interactive terminal:

```bash
goscanner
```

The device bar and device list show a **bold model**, an IP address, and the current protocol. IP addresses and protocols use the current theme’s information color. Normal use does not require protocol selection. Without discovery metadata, the model is shown as unknown; later discovery fills it in. A saved device is not necessarily ready. It is validated again when selected.

| Key | Action |
| --- | --- |
| Enter | Start scanning when ready; continue with the next page while waiting |
| `r` | Recheck device status and options |
| `s` | Edit settings for the scan |
| `d` | Switch devices: select a saved device, enter an IP address, or start discovery |
| `p` | Change and save the selected device's preferred protocol in the device list |
| Esc | Go back, end page changes, or exit, as indicated by the current screen |
| Ctrl+C | Cancel the current operation and wait for cleanup |

The settings page lists all options. Use ↑/↓ to select an item, Enter to open its choices, and Space to toggle a switch. Resolution, color, and format choices come from device capabilities. Only the output path and custom scan area require text input. “Interface language” offers automatic detection, `English`, or `简体中文`. Language names use their native form. “Interface theme” offers automatic detection, light background, or dark background. An explicit `--theme` overrides the saved preference. Select “Save and apply” to save all visible settings, return to the scan page, and recheck the device. This does not start a scan. Esc discards changes without writing configuration. Only the output directory and format are saved; the next run generates a new filename. A command-line `--lang` still overrides the saved preference. Without that option, confirming a language change switches the interface immediately.

### `discover`: find devices

```bash
goscanner discover
goscanner discover --timeout 5s
```

Discovers eSCL (Bonjour) and WSD (WS-Discovery) scanners on the local network in parallel. Results include the model, name, protocol, and full scan endpoint. Records merge only when comparable identity information, such as a UUID or valid serial number, confirms they are the same device. The same IP address or a similar model is not sufficient. Unconfirmed identities remain separate devices. When both protocols are first confirmed, eSCL becomes the preference and both addresses are saved. Later discovery and capability refreshes preserve a protocol preference set with `p`; connection failure does not switch protocols. The default timeout is **10 seconds**. Discovery does not select a device, change the default device, or start a scan.

Normal mode records discovery results in `drives.json`. An interactive terminal shows a results interface. Plain-text mode writes a JSON array to stdout and warnings to stderr. Use `--agent discover` for a stable machine interface.

If no device is found, check whether it is awake, Wi-Fi client isolation, VPN settings, firewalls, and macOS local network permissions. An IP address alone still requires the device registry or discovery to resolve a service address. If you know the full service address, use the advanced `info` options to query it. They cannot bypass network or system permission restrictions.

### `info`: read capabilities and status

```bash
goscanner info
goscanner info --host 192.168.1.8
# Optional: use WSD for this operation without changing the saved preference.
goscanner info --host 192.168.1.8 --protocol wsd
```

Reads device status, supported formats, colors, DPI, and scan dimensions without creating a scan job. The default timeout is **2 minutes**. Normal mode records successful queries in the device registry. The TUI saves a new default device only after selection and successful validation.

`info` and `scan` share these common options:

| Option | Description |
| --- | --- |
| `--host IP` | Resolve the actual service address from the device registry or discovery. URLs, hostnames, and credentials are not accepted |
| `--protocol NAME` | Optional `wsd` or `escl`, for this operation only. Otherwise use the registered preference; this does not change the saved preference |
| `--timeout DURATION` | Timeout for this command, such as `10s` or `2m` |

If the full service address is known, use the advanced `--endpoint` option, or `--host` with `--port`, `--path`, and `--scheme`. See `goscanner info --help` for all options and mutual-exclusion rules. Normal device registration does not need these options.

Without an explicit address, configuration identifies the default device and the registry supplies its preferred protocol. Explicit CLI `--protocol` overrides this operation only. With no default device, the TUI opens the device list. Manual registration takes only an IP address: discovery resolves the actual port and path before validation and saving. Failed discovery does not trigger address guessing. Connection failure does not switch to another device or protocol.

### `scan`: scan and save

```bash
# Scan pages and merge them into a PDF when finished.
goscanner scan -o 文档.pdf

# Scan one page and save it as PNG.
goscanner scan --manual=false -o 单页.png

# Select grayscale, DPI, and the full flatbed area according to device capabilities.
goscanner scan --dpi 300 --mode gray --area full -o 灰度.tiff

# Scan pages and save each as a separate PDF.
goscanner scan --single-page -o 分页.pdf
```

| Option | Description and built-in default |
| --- | --- |
| `--dpi DPI` | Resolution; default: `150`. Must be supported by the device |
| `--theme MODE` | TUI theme: `auto`, `light`, or `dark`; auto falls back to dark; accepted before or after the command |
| `--mode MODE` | `color` or `gray`; default: `color` |
| `--area AREA` | `a4`, `full`, or `x,y,width,height`; default: `a4`. Custom coordinates are in millimeters, measured from the flatbed's upper-left corner |
| `-o FILE` | Output path; supports `.jpg`, `.jpeg`, `.tif`, `.tiff`, `.png`, and `.pdf` |
| `--manual[=BOOL]` | Manual page changes. When not configured, defaults to on in the TUI and off in plain-text mode. Use `--manual=false` to disable |
| `--single-page` | Save each PDF page separately; PDF output only. Off by default |
| `--deskew` | AI deskew; off by default. Retains an additional device original when enabled |
| `--autocrop` | AI crop; off by default. Retains an additional device original when enabled |
| `--timeout DURATION` | Timeout per scan page; default: `2m0s`. Excludes device selection and page-change waits |

Before scanning, the TUI checks for `Idle` status and validates the current options. Enter is still required to start. Readiness includes a check timestamp, not a device reservation; capabilities are read and validated again before the actual scan. A valid but busy device address can be saved, but scanning remains disabled. The program never automatically retries scan jobs.

In plain-text mode, `--manual` or a saved `scan.manual: true` enables page-change waits. Press any key to continue or Esc to finish; this mode requires an interactive terminal. Piping output does not turn it into Agent mode.

#### Default filenames

Without `-o`, output defaults to the command's current working directory. The filename stem is **`scan-YYYYMMDD-HHMMSS`**, with one-second precision. A single continuous scanning session uses one stem:

| Output mode | Example filename |
| --- | --- |
| Single image | `scan-20261104-143025.jpg` |
| Continuous images | `scan-20261104-143025-001.jpg`, `…-002.jpg` |
| Merged PDF | `scan-20261104-143025.pdf` |
| Continuous scanning with a PDF per page | `scan-20261104-143025-001.pdf`, `…-002.pdf` |

With `-o 文档.pdf`, the custom stem is used. Separate pages become `文档-001.pdf` and `文档-002.pdf`. AI originals use the `-original` marker; for example, continuous scan originals are named `文档-original-001.jpg`. For WSD PNG output, the original is TIFF. eSCL original extensions follow the actual source format. PDF originals are JPEG.

Configuration can change the directory and format. Runs within the same second can collide if they use the same output path. The program reports an error rather than overwriting files or skipping occupied page numbers.

### `--agent`: scripts and Agents

**Place `--agent` before the subcommand**:

```bash
goscanner --agent discover
goscanner --agent info --host 192.168.1.8
goscanner --agent scan --host 192.168.1.8 -o 单页.pdf
goscanner --agent scan --help
```

- It does not read the keyboard, start the TUI, or modify configuration or the device registry.
- An unregistered IP address filters discovery results by IP. Without an explicit address or default device, the program returns `device_required`; it does not discover or select a device automatically.
- Interactive page changes are not supported. Each `scan` performs one acquisition. If manual scanning is saved as enabled, explicitly pass `--manual=false`.
- Stdout contains exactly one compact JSON object, without progress logs or colors.
- Project-generated fields, statuses, error explanations, warnings, and help always use English. Original external content is not forcibly translated.
- `status` is `ok`, `error`, or `partial`. Failure and partial completion return nonzero exit codes. Check `files`, `originals`, `retained`, and other fields to identify files actually kept.

Successful result excerpt:

```json
{ "status": "ok", "files": ["/absolute/path/document.pdf"], "pages": 1 }
```

See [Human and Agent interfaces (Chinese)](docs/interfaces.md) for fields, error codes, exit behavior, and partial-completion rules.

### File safety and troubleshooting

Files are staged in the output directory and published only after complete validation. Incomplete images are not treated as final files. The output filesystem must support hard links. Images, PDFs, and originals use permissions `0600`; newly created output and image staging directories use `0700`.

| Situation | Action |
| --- | --- |
| Unsupported option or format | Run `info` to check capabilities, then adjust DPI, area, color, or source format. Capability lists do not guarantee every combination |
| Scan timeout | Check device status and adjust `--timeout` if necessary. Do not repeatedly start jobs |
| Image publication or PDF save failure | Find the retained images, `.part` file, or PDF staging directory reported by the error. Back them up before further action |
| Incorrect deskew or missing content after cropping | Check the additional original. Use `--deskew=false --autocrop=false` to disable processing |
| Switching devices | Press `d` in the TUI. Cancellation or failed validation does not replace the previous device |

Ctrl+C or SIGTERM attempts to cancel this client's job when its ID is known. If the connection fails during job creation before an ID is received, the program cannot reliably cancel the unknown job. Merged PDF output attempts to save completed pages on normal completion, interruption, or a later-page failure. Forced termination and power loss do not guarantee saving. Cleanup can take up to 10 additional seconds; filesystem operations and local image processing are not hard real-time operations.

Scan images and AI inference are processed locally, without uploading them to a cloud inference service. WSD uses unencrypted HTTP: use it only on a trusted LAN and do not expose scan ports to the internet. Sync folders such as iCloud and Google Drive can still upload output files. Save sensitive documents in a nonsynced directory.

For device-specific problems, open a [GitHub issue](https://github.com/johnbitcn/GoScanner/issues) with the device model, system and package versions, command, error details, and sanitized `discover` / `info` output. Do not include job tokens, sensitive documents, or unsanitized network information.

## Configuration files

No initial configuration file is required. Select a device in the TUI. After successful validation, the program saves its default reference as `device.record_id`. The device registry maintains protocol and address details; you do not need to enter or synchronize them manually.

| File | Purpose |
| --- | --- |
| `~/.config/goscanner/config.json` | Interface language, default scanner reference, scan options, output directory and format, and AI switches |
| `~/.config/goscanner/drives.json` | Device identities, protocol addresses, preferences, and historical capability snapshots; not proof of current readiness |

Specify only the settings you want to change. For example, save PDFs to a chosen directory by default:

```json
{
  "output": {
    "directory": "~/Documents/Scans",
    "format": "pdf"
  }
}
```

Modify only the required fields in an existing configuration; do not overwrite it with this example. Scan settings follow **explicit command-line options > configuration > built-in defaults**. “Save and apply” in TUI settings persists visible values; Esc discards them. Agent mode does not write configuration or the device registry.

See [Configuration (Chinese)](docs/configuration.md) for all fields, directory expansion, and reference errors; [eSCL and WSD (Chinese)](docs/escl.md) for protocols, identity merging, HTTPS certificates, and network restrictions; and [Build from source (Chinese)](docs/build.md) for source builds and the AI runtime environment.

## License

Original project code is licensed under the [Apache License 2.0](LICENSE). Third-party libraries and AI models retain their own licenses. The npm package includes license texts and notices in `runtime/licenses/`. See [npm documentation (Chinese)](docs/npm.md#固定构建输入与许可) for source and distribution details.
