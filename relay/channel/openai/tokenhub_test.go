package openai

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenHubImageRequestUsesOpenAICompatiblePath(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeTokenHub,
			ChannelBaseUrl: "https://provider.example",
		},
		RequestURLPath: "/v1/images/generations",
	}
	adaptor.Init(info)

	requestURL, err := adaptor.GetRequestURL(info)

	require.NoError(t, err)
	assert.Equal(t, "https://provider.example/v1/images/generations", requestURL)
}
