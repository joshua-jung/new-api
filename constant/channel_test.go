package constant

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtensionChannelDefaultBaseURLIsBoundsSafe(t *testing.T) {
	assert.Equal(t, 10000, ChannelTypeTokenHub)
	assert.Empty(t, GetDefaultChannelBaseURL(ChannelTypeTokenHub))
	assert.Equal(t, ChannelBaseURLs[ChannelTypeOpenAI], GetDefaultChannelBaseURL(ChannelTypeOpenAI))
}
