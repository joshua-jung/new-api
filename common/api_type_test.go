package common

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenHubUsesOpenAICompatibleAPIAdaptor(t *testing.T) {
	apiType, ok := ChannelType2APIType(constant.ChannelTypeTokenHub)

	require.True(t, ok)
	assert.Equal(t, constant.APITypeOpenAI, apiType)
}
