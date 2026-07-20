package auth

import "hash/fnv"

// User — участник. ID это либо Discord ID, либо "g<hex>" для гостей.
type User struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Avatar string `json:"avatar,omitempty"` // URL, для гостей пусто — клиент рисует кружок с буквой
	Color  Color  `json:"color"`
	Guest  bool   `json:"guest,omitempty"`
}

type Color struct {
	Name string `json:"name"`
	Hex  string `json:"hex"`
}

var (
	White   = Color{"white", "#FFFFFF"}
	Red     = Color{"red", "#EF4444"}
	Orange  = Color{"orange", "#F97316"}
	Amber   = Color{"amber", "#F59E0B"}
	Yellow  = Color{"yellow", "#EAB308"}
	Lime    = Color{"lime", "#84CC16"}
	Green   = Color{"green", "#22C55E"}
	Emerald = Color{"emerald", "#10B981"}
	Teal    = Color{"teal", "#14B8A6"}
	Cyan    = Color{"cyan", "#06B6D4"}
	Sky     = Color{"sky", "#0EA5E9"}
	Blue    = Color{"blue", "#3B82F6"}
	Indigo  = Color{"indigo", "#6366F1"}
	Violet  = Color{"violet", "#8B5CF6"}
	Purple  = Color{"purple", "#A855F7"}
	Fuchsia = Color{"fuchsia", "#D946EF"}
	Pink    = Color{"pink", "#EC4899"}
	Rose    = Color{"rose", "#F43F5E"}
	Brown   = Color{"brown", "#92400E"}
	Gray    = Color{"gray", "#6B7280"}
	Slate   = Color{"slate", "#475569"}
	Zinc    = Color{"zinc", "#52525B"}
)

// Colors — только яркие, читаемые на тёмном фоне (для цвета ников).
var Colors = []Color{
	Red, Orange, Amber, Yellow, Lime, Green, Emerald, Teal, Cyan,
	Sky, Blue, Indigo, Violet, Purple, Fuchsia, Pink, Rose,
}

// ColorFor — стабильный цвет по ID, чтобы у юзера цвет не менялся между сессиями.
func ColorFor(id string) Color {
	h := fnv.New32a()
	h.Write([]byte(id))
	return Colors[h.Sum32()%uint32(len(Colors))]
}
