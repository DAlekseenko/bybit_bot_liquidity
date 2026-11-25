package paperwire

import "encoding/json"

// EncodePortfolioState выдаёт проверенное финансовое состояние с точными рациональными суммами.
func EncodePortfolioState(s PortfolioState) ([]byte, error) { return json.Marshal(stateOut(s)) }
