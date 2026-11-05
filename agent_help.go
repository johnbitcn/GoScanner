package main

import "fmt"

func agentHelpContent(command string) (string, []helpSection) {
	if command == "" {
		return "macOS LAN scanner client for eSCL and WSD", []helpSection{
			{"Usage", []helpItem{{text: "goscanner --agent <command> [options]"}}},
			{"Commands", []helpItem{
				{"scan", "Scan a document without interaction.", ""},
				{"info", "Read scanner capabilities and status without scanning.", ""},
				{"discover", "Discover LAN scanners without changing the default scanner.", ""},
			}},
			{"Examples", []helpItem{{text: "goscanner --agent discover"}, {text: "goscanner --agent info"}, {text: "goscanner --agent scan -o document.pdf"}}},
			{"Global options", []helpItem{
				{"--agent", "Noninteractive JSON output. Must precede the command.", ""},
				{"-h, --help", "Show help without reading configuration or accessing scanners.", ""},
			}},
			{"Configuration", []helpItem{{text: "~/.config/goscanner/config.json"}}},
			{"Command options", []helpItem{{text: "goscanner --agent <command> --help"}}},
		}
	}
	defaults := defaultConfig()
	summary := map[string]string{
		"scan":     "Scan a document without interaction",
		"info":     "Read scanner capabilities and status without starting a scan",
		"discover": "Discover eSCL and WSD scanners on the LAN in parallel",
	}[command]
	sections := []helpSection{{"Usage", []helpItem{{text: "goscanner --agent " + command + " [options]"}}}}
	if command != "discover" {
		sections = append(sections, helpSection{"Scanner", []helpItem{
			{"--host IP", "Scanner IP address, not a hostname or URL. An unregistered IP is resolved through discovery; no port or path is guessed.", ""},
			{"--protocol NAME", "wsd or escl. Overrides this operation only. Otherwise use the registered preference; eSCL is preferred when both protocols are first confirmed.", "registered preference"},
			{"--scheme NAME", "http or https. WSD requires http. eSCL supports https with normal certificate verification.", "http"},
			{"--port PORT", "Known scan service port. Use with --host.", ""},
			{"--path PATH", "Known scan service path starting with /. Use with --host. An IP alone is resolved through discovery.", ""},
			{text: "Specify a scanner address or configure a default scanner record. With neither, return device_required without discovery or automatic selection."},
		}})
	}
	if command == "scan" {
		sections = append(sections,
			helpSection{"Scan", []helpItem{
				{"--dpi DPI", "Scan resolution. Must be supported by the scanner.", fmt.Sprint(defaults.Scan.DPI)},
				{"--mode MODE", "color or gray.", defaults.Scan.Mode},
				{"--area AREA", "a4, full, or x,y,width,height in millimeters. The origin is the flatbed top-left corner.", defaults.Scan.Area},
			}},
			helpSection{"Output", []helpItem{
				{"-o FILE", "JPEG, TIFF, PNG, or PDF output. The extension selects the format. Without -o, generate a timestamped filename using configuration.", ""},
				{"--single-page", "Save each PDF page as a separate document. Requires PDF output.", "false"},
			}},
			helpSection{"Interaction", []helpItem{
				{"--manual[=BOOL]", "Agent mode rejects manual scanning. Use --manual=false to override configuration, and run scan separately for each acquisition.", "false"},
			}},
			helpSection{"Image processing", []helpItem{
				{"--deskew", "AI deskew. Also retain the scanner original image.", "false"},
				{"--autocrop", "AI crop. Also retain the scanner original image.", "false"},
				{text: "Use --deskew=false or --autocrop=false to disable processing enabled in configuration."},
			}},
		)
	}
	timeout := helpItem{"--timeout DURATION", "Timeout for reading scanner capabilities and status.", "2m"}
	if command == "scan" {
		timeout.text, timeout.defaultValue = "Timeout per scan page. Failure cleanup may take up to 10 additional seconds.", defaultScanTimeout.String()
	}
	if command == "discover" {
		timeout.text, timeout.defaultValue = "LAN scanner discovery timeout.", "10s"
	}
	sections = append(sections, helpSection{"Timeout", []helpItem{timeout}})
	if command != "discover" {
		sections = append(sections, helpSection{"Advanced address", []helpItem{
			{"--endpoint URL", "Full HTTP/HTTPS scan endpoint. Registered scanners use their preferred protocol unless --protocol overrides it. Do not combine with split address options.", ""},
		}})
	}
	sections = append(sections, helpSection{"Help", []helpItem{{"-h, --help", "Show help for this command.", ""}}})
	switch command {
	case "scan":
		sections = append(sections,
			helpSection{"Examples", []helpItem{{text: "goscanner --agent scan -o document.pdf"}, {text: "goscanner --agent scan --manual=false -o page.png"}}},
			helpSection{"Behavior", []helpItem{
				{text: "Without -o, the built-in output is scan-YYYYMMDD-HHMMSS.jpg in the current directory. Configuration can change the directory and format."},
				{text: "WSD uses native uncompressed TIFF; PNG is converted losslessly from TIFF. eSCL selects an image format from scanner capabilities. Converting JPEG to PNG cannot recover compression loss."},
			}},
			helpSection{"Safety", []helpItem{
				{text: "Existing files are never overwritten. Connection failure does not switch protocols. Scan jobs are never automatically retried. Ctrl+C attempts to cancel this client's job."},
				{text: "Merged PDF output attempts to save completed pages on normal completion, interruption, or a later-page failure."},
				{text: "A connection failure during job creation does not prove that no job was started. Check the scanner state before retrying."},
			}},
		)
	case "info":
		sections = append(sections,
			helpSection{"Examples", []helpItem{{text: "goscanner --agent info"}, {text: "goscanner --agent info --host 192.168.1.8"}}},
			helpSection{"Behavior", []helpItem{{text: "Read scanner capabilities and status without starting a scan."}}},
		)
	case "discover":
		sections = append(sections,
			helpSection{"Examples", []helpItem{{text: "goscanner --agent discover"}, {text: "goscanner --agent discover --timeout 5s"}}},
			helpSection{"Behavior", []helpItem{{text: "Discover scanners without starting a scan, selecting a scanner, or changing the default scanner."}}},
		)
	}
	sections = append(sections, helpSection{"Agent protocol", []helpItem{
		{text: "--agent must precede the command. Return one JSON result without interaction or progress output. Do not write configuration or the scanner registry."},
		{text: "Use status, error.code, files, originals, and retained to interpret results. Do not infer retry safety from an error message alone. A partial result still has a nonzero exit code."},
	}})
	if command != "discover" {
		sections = append(sections, helpSection{"Configuration", []helpItem{
			{text: "~/.config/goscanner/config.json"},
			{text: "Scan options: explicit command-line options override configuration, which overrides built-in defaults. device.record_id references the scanner registry. The registered protocol preference is used by default."},
			{text: "An invalid record reference returns device_record_not_found. A confirmed identity conflict returns device_identity_conflict. Select the scanner again; no automatic rebinding or rescan is performed."},
		}})
	}
	return summary, sections
}
