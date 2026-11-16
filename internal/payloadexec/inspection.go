package payloadexec

import "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadwire"

const InspectionVersion = payloadwire.InspectionVersion
const MaxInspectionRequest = payloadwire.MaxInspectionRequest

type InspectionRequest = payloadwire.InspectionRequest

func ParseInspectionRequest(raw []byte) (InspectionRequest, error) {
	return payloadwire.ParseInspectionRequest(raw)
}
