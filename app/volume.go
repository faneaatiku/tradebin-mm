package app

type volumeConfig interface {
}

type Volume struct {
	cfg volumeConfig
}

func NewVolumeMaker(cfg volumeConfig) (*Volume, error) {
	return &Volume{}, nil
}

func (v *Volume) MakeVolume() error {
	//TODO implement me
	panic("implement me")
}
