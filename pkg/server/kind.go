package server

// normalizeKind maps an unrecognized proxy kind onto LUMO's own vocabulary by
// capability rather than by name: a proxy that carries a secret key and does
// not claim a public port is a visitor-style tunnel, whatever the client
// called it. That keeps older clients working without the server carrying
// any legacy type name of its own.
func normalizeKind(kind, sk string, remotePort int) (string, bool) {
	if sk != "" && remotePort == 0 {
		return "sealed-tcp", true
	}
	return "", false
}
