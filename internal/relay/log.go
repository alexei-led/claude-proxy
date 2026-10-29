package relay

func boundedLabel(value string) string {
	const maxLabelBytes = 128
	if len(value) > maxLabelBytes {
		return value[:maxLabelBytes] + "..."
	}
	return value
}
