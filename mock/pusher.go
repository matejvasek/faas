package mock

type Pusher struct {
	PushInvoked bool
	PushFn      func() error
}

func NewPusher() *Pusher {
	return &Pusher{
		PushFn: func() error { return nil },
	}
}

func (i *Pusher) Push() error {
	i.PushInvoked = true
	return i.PushFn()
}
