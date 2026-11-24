// Команда webmail — ядро со встроенными открытыми адаптерами (standalone).
// Адаптер включается только секцией adapters.<имя> с enabled: true.
package main

import (
	"github.com/ibuben/squareball/adapters/caldav"
	"github.com/ibuben/squareball/adapters/carddav"
	"github.com/ibuben/squareball/adapters/panel"
	"github.com/ibuben/squareball/app"
)

func main() { app.Main(carddav.New(), caldav.New(), panel.New()) }
