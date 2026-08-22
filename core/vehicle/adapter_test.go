package vehicle

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSocGradientStore(t *testing.T) {
	v := testAdapter("soc-gradient")

	// nothing stored yet
	_, ok := v.GetSocGradient()
	assert.False(t, ok)

	require.NoError(t, v.SetSocGradient(437.5))

	got, ok := v.GetSocGradient()
	assert.True(t, ok)
	assert.Equal(t, 437.5, got)

	// overwriting replaces the stored value
	require.NoError(t, v.SetSocGradient(500))

	got, ok = v.GetSocGradient()
	assert.True(t, ok)
	assert.Equal(t, 500.0, got)
}

func TestDummySocGradient(t *testing.T) {
	v := &dummy{}

	_, ok := v.GetSocGradient()
	assert.False(t, ok)

	require.NoError(t, v.SetSocGradient(500))

	// dummy never persists
	_, ok = v.GetSocGradient()
	assert.False(t, ok)
}
