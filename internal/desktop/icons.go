package desktop

import "fyne.io/fyne/v2"

// Свои иконки: в наборе Fyne нет ни микрофона, ни наушников, а мут и «глухота»
// штатными иконками громкости выглядели одинаково — по кнопке было не понять,
// что именно ты выключил.
//
// Состояние показывает сама иконка (перечёркнута или нет), поэтому подписи
// рядом с кнопками не нужны.

func svgIcon(name, body string) fyne.Resource {
	return fyne.NewStaticResource(name, []byte(
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" `+
			`stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">`+
			body+`</svg>`))
}

const strikeLine = `<line x1="3" y1="3" x2="21" y2="21" stroke-width="2.4"/>`

var (
	iconMicOn = svgIcon("mic-on.svg",
		`<rect x="9" y="2" width="6" height="11" rx="3"/>`+
			`<path d="M5 10a7 7 0 0 0 14 0"/>`+
			`<line x1="12" y1="17" x2="12" y2="21"/>`+
			`<line x1="8" y1="21" x2="16" y2="21"/>`)

	iconMicOff = svgIcon("mic-off.svg",
		`<rect x="9" y="2" width="6" height="11" rx="3"/>`+
			`<path d="M5 10a7 7 0 0 0 14 0"/>`+
			`<line x1="12" y1="17" x2="12" y2="21"/>`+
			`<line x1="8" y1="21" x2="16" y2="21"/>`+strikeLine)

	iconSoundOn = svgIcon("sound-on.svg",
		`<path d="M4 15V9h4l5-4v14l-5-4H4z"/>`+
			`<path d="M17 8.5a5 5 0 0 1 0 7"/>`+
			`<path d="M19.5 6a8.5 8.5 0 0 1 0 12"/>`)

	iconSoundOff = svgIcon("sound-off.svg",
		`<path d="M4 15V9h4l5-4v14l-5-4H4z"/>`+
			`<path d="M17 8.5a5 5 0 0 1 0 7"/>`+
			`<path d="M19.5 6a8.5 8.5 0 0 1 0 12"/>`+strikeLine)
)
