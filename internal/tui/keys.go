package tui

func keyHelp(mode viewMode, searching, showHelp, confirmQuit bool) string {
	if searching {
		return "Enter apply search  ·  Esc close search"
	}
	if confirmQuit {
		return ""
	}
	if showHelp {
		return "↑/↓ or j/k scroll  ·  PgUp/PgDn page  ·  h or Esc close help  ·  q quit"
	}
	if mode == viewDetail {
		return "n/p recent heights  ·  Tab switch tab  ·  ↑/↓ or j/k scroll  ·  b or Esc back  ·  / search  ·  f severity filter  ·  Space pause  ·  h help  ·  q quit"
	}

	if mode == viewDashboard {
		return "↑/↓ or j/k select incident  ·  Enter open detail  ·  / search  ·  f severity filter  ·  Space pause  ·  h help  ·  q quit"
	}

	return "↑/↓ or j/k select incident  ·  Enter open detail  ·  / search  ·  f severity filter  ·  Space pause  ·  h help  ·  q quit"
}
