package provider

type unprivilegedBubblewrapUser interface {
	UsesUnprivilegedBubblewrapInDocker() bool
}

func (*Claude) UsesUnprivilegedBubblewrapInDocker() bool { return true }

func UsesUnprivilegedBubblewrap(adapter Adapter) bool {
	user, ok := adapter.(unprivilegedBubblewrapUser)
	return ok && user.UsesUnprivilegedBubblewrapInDocker()
}
