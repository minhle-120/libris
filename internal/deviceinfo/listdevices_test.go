package deviceinfo

import "testing"

// The fixture is the shape common/arg.cpp:1149 emits — read from that function, not from a
// remembered transcript of running the tool.
const listDevicesTwo = `Available devices:
  Metal: Apple M2 Pro (16384 MiB, 8192 MiB free)
  CPU: Intel(R) Core(TM) i7-14700K (65166 MiB, 52000 MiB free)
`

const listDevicesNone = `Available devices:
  (none)
`

func TestParseDevicesRealOutput(t *testing.T) {
	got := ParseDevices([]byte(listDevicesTwo))
	if len(got) != 2 {
		t.Fatalf("want 2 devices, got %d: %+v", len(got), got)
	}

	metal := got[0]
	if metal.Name != "Metal" {
		t.Errorf("name = %q, want the ggml slot name", metal.Name)
	}
	if metal.Kind != Apple {
		t.Errorf("kind = %q, want apple from the description", metal.Kind)
	}
	if metal.TotalBytes != 16384*mib {
		t.Errorf("total = %d, want 16384 MiB", metal.TotalBytes)
	}
	if metal.FreeBytes != 8192*mib {
		t.Errorf("free = %d, want 8192 MiB: the second number is what is free right now", metal.FreeBytes)
	}
	if metal.Description != "Apple M2 Pro" {
		t.Errorf("description = %q, want the description without the memory suffix", metal.Description)
	}

	// The CPU line is the ggml CPU backend, and its description is the CPU model string —
	// "Intel(R) Core(TM) i7-14700K" — which name-matching alone would report as an Intel
	// GPU. The slot name has to win.
	if got[1].Kind != CPU {
		t.Errorf("cpu kind = %q, want cpu", got[1].Kind)
	}
}

func TestParseDevicesNoneIsAnAnswer(t *testing.T) {
	if got := ParseDevices([]byte(listDevicesNone)); got != nil {
		t.Errorf("(none) parsed as %v, want nil: a build without GPU support is telling us something", got)
	}
	if got := ParseDevices(nil); got != nil {
		t.Errorf("empty output parsed as %v, want nil", got)
	}
}

func TestParseDevicesVulkanNamesTheCard(t *testing.T) {
	const out = `Available devices:
  Vulkan0: NVIDIA GeForce RTX 3070 (8192 MiB, 7600 MiB free)
  Vulkan1: AMD Radeon RX 6600 (8192 MiB, 7000 MiB free)
`
	got := ParseDevices([]byte(out))
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Kind != Nvidia || got[1].Kind != AMD {
		t.Errorf("kinds = %q, %q — the Vulkan backend says nothing about the vendor, so the card's own string must", got[0].Kind, got[1].Kind)
	}
}
