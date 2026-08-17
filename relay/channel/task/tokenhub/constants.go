package tokenhub

type videoProtocol string

const seedanceGenerationsV1 videoProtocol = "seedance_generations_v1"

var ModelList = []string{
	"doubao-seedance-2-0-260128",
	"doubao-seedance-2-0-fast-260128",
	"doubao-seedance-2-0-mini-260615",
	"doubao-seedance-2-5-cloud",
	"doubao-seedance-2-5-260628",
}

var modelVideoProtocols = map[string]videoProtocol{
	"doubao-seedance-2-0-260128":      seedanceGenerationsV1,
	"doubao-seedance-2-0-fast-260128": seedanceGenerationsV1,
	"doubao-seedance-2-0-mini-260615": seedanceGenerationsV1,
	"doubao-seedance-2-5-cloud":       seedanceGenerationsV1,
	"doubao-seedance-2-5-260628":      seedanceGenerationsV1,
}

const ChannelName = "tokenhub"
