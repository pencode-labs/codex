package main

// Closing the view detaches this client; stopping a turn is an explicit action.
func (a *agent) requestCloseWorkspace() error {
	return a.publisher.CloseUI("composer")
}
