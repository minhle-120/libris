//go:build windows

package deviceinfo

import (
	"context"
)

// Windows answers the whole question with one CIM query, which is why there is no fallback
// chain here the way Linux has one.
//
// Win32_VideoController is served by WMI on every supported edition, so this needs no
// driver-specific tool, no administrator right, and no dependency. pwsh is tried first
// because PowerShell 7 is what a developer box has, and `powershell` because a plain
// Windows 11 install has only Windows PowerShell — and both spell the same query.
//
// AdapterRAM is capped at 4 GiB by the class itself (see parseWinVideo): on an 8 GB card it
// reports its ceiling, not the card. That is why the Vulkan probe inside llama.cpp, which
// reports memory honestly, is the source that will set a launch's parameters — this one
// only tells a person what they have before they download.
func collect(ctx context.Context, o Options) []Device {
	const query = `Get-CimInstance Win32_VideoController | ` +
		`Select-Object Name,AdapterRAM,DriverVersion,PNPDeviceID | ConvertTo-Json -Compress`

	for _, shell := range []string{"pwsh", "powershell"} {
		if _, err := o.LookPath(shell); err != nil {
			continue
		}
		out, err := o.Run(ctx, shell, "-NoProfile", "-NonInteractive", "-Command", query)
		if err != nil {
			continue
		}
		if rows := parseWinVideo(out); rows != nil {
			return rows
		}
	}
	return nil
}
