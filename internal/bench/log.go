package bench

func (s Store) logLimits() (per, total uint64, files int, err error) {
	return s.workerStorage().LogLimits()
}
func (s Store) reserveLog(db database) (uint64, error) {
	if err := s.validate(); err != nil {
		return 0, err
	}
	return s.workerStorage().ReserveLog(db)
}
