package tui

func keyHelp(mode viewMode, searching bool) string {
	if searching {
		return "Enter apply search  ·  Esc close search"
	}

	if mode == viewDetail {
		return "n/p recent heights  ·  Tab switch tab  ·  ↑/↓ or j/k scroll  ·  b or Esc back  ·  / search  ·  f severity filter  ·  Space pause  ·  q quit"
	}

	return "↑/↓ or j/k select incident  ·  Enter open detail  ·  / search  ·  f severity filter  ·  Space pause  ·  q quit"
}
