package desktop

import "math"

// Спектральное шумоподавление на чистом Go — тот же принцип, что у RNNoise в
// OBS: оцениваем спектр постоянного фона (вентилятор, гул, шипение) и вычитаем
// его из каждого кадра, оставляя голос. В отличие от порога тишины, чистит звук
// ПОКА ты говоришь, а не только режет паузы.
//
// Никаких нативных зависимостей: маленький FFT здесь же. Cgo-обёртки настоящего
// RNNoise сломали бы статическую сборку под винду, а это дороже, чем разница в
// качестве на фоне вентилятора.
//
// Работает потоково: перекрывающиеся окна 1024 отсчёта с шагом 512 (50%),
// анализ и синтез корнем окна Ханна — это даёт чистую overlap-add реконструкцию
// без щелчков. Задержка ~10 мс, нагрузка — около 200 FFT в секунду, мелочь.

const (
	nsFFT    = 1024
	nsHop    = 512
	nsBins   = nsFFT/2 + 1
	overSub  = 1.5  // во сколько раз вычитаем оценку шума (over-subtraction)
	floor    = 0.08 // не глушим бин в ноль — иначе «музыкальный» шум
	nsWarmup = 30   // кадров на первичную оценку фона (без VAD)
)

type denoiser struct {
	win    [nsFFT]float64 // корень окна Ханна (и анализ, и синтез)
	inBuf  []float64      // непрерывный вход
	outBuf []float64      // overlap-add выход, ещё не выданный
	tail   int            // сколько в outBuf уже досуммировано и готово к выдаче
	outPCM []int16        // готовые отсчёты в очереди на возврат

	noise  [nsBins]float64 // оценка спектра шума по бинам
	gain   [nsBins]float64 // сглаженное усиление по бинам
	frames int             // сколько кадров обработано (для прогрева)

	re [nsFFT]float64
	im [nsFFT]float64
}

func newDenoiser() *denoiser {
	d := &denoiser{}
	for i := range d.win {
		// sqrt(Hann): произведение анализа и синтеза даёт окно Ханна, а оно
		// суммируется в константу при 50% перекрытии — идеальная реконструкция
		d.win[i] = math.Sqrt(0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(nsFFT)))
	}
	for i := range d.gain {
		d.gain[i] = 1
	}
	return d
}

// Process принимает кадр микрофона и возвращает столько же очищенных отсчётов
// (после короткого прогрева — ровно len(frame)). Вход не портит.
func (d *denoiser) Process(frame []int16) []int16 {
	for _, s := range frame {
		d.inBuf = append(d.inBuf, float64(s)/32768)
	}
	for len(d.inBuf) >= nsFFT {
		d.step()
		d.inBuf = d.inBuf[nsHop:]
	}
	// отдаём столько, сколько накопилось; на первом кадре добьём тишиной,
	// чтобы поток не сбивался с ритма 960-отсчётных кадров
	out := make([]int16, len(frame))
	n := len(d.outPCM)
	if n > len(out) {
		n = len(out)
	}
	copy(out, d.outPCM[:n])
	d.outPCM = d.outPCM[n:]
	return out
}

func (d *denoiser) step() {
	for i := 0; i < nsFFT; i++ {
		d.re[i] = d.inBuf[i] * d.win[i]
		d.im[i] = 0
	}
	fft(d.re[:], d.im[:], false)

	// сначала считаем спектр и грубо решаем, есть ли речь: если суммарная
	// энергия заметно выше оценки фона — говорят. Это ключевой момент: во время
	// речи оценку шума наверх НЕ двигаем, иначе она заползёт на голос и выест его.
	var mag [nsBins]float64
	var sumMag, sumNoise float64
	for k := 0; k < nsBins; k++ {
		mag[k] = math.Hypot(d.re[k], d.im[k])
		sumMag += mag[k]
		sumNoise += d.noise[k]
	}
	// Прогрев: первые кадры оценка фона с нуля, поэтому «речь ли это» решать
	// ещё нельзя — просто копим средний спектр как фон. Без этого noise остаётся
	// нулём, усиление всегда 1, и денойзер ничего не делает.
	warming := d.frames < nsWarmup
	d.frames++
	speech := !warming && sumMag > 1.5*sumNoise+1e-6

	for k := 0; k < nsBins; k++ {
		m := mag[k]
		switch {
		case warming:
			d.noise[k] += m / float64(nsWarmup) // усредняем стартовый фон
		case m < d.noise[k]:
			d.noise[k] = 0.7*d.noise[k] + 0.3*m // тише фона — следуем вниз
		case !speech:
			d.noise[k] = 0.98*d.noise[k] + 0.02*m // пауза — медленно уточняем фон
		}
		// во время речи фон не трогаем вовсе

		g := 1.0
		if m > 1e-9 {
			g = (m - overSub*d.noise[k]) / m
		}
		if g < floor {
			g = floor
		} else if g > 1 {
			g = 1
		}
		d.gain[k] = 0.5*d.gain[k] + 0.5*g // сглаживаем во времени
	}

	// применяем усиление симметрично (бины выше половины — зеркало)
	for k := 0; k < nsBins; k++ {
		d.re[k] *= d.gain[k]
		d.im[k] *= d.gain[k]
		if k > 0 && k < nsFFT/2 {
			j := nsFFT - k
			d.re[j] *= d.gain[k]
			d.im[j] *= d.gain[k]
		}
	}
	fft(d.re[:], d.im[:], true)

	// overlap-add с синтезирующим окном
	base := d.tail
	for len(d.outBuf) < base+nsFFT {
		d.outBuf = append(d.outBuf, 0)
	}
	for i := 0; i < nsFFT; i++ {
		d.outBuf[base+i] += d.re[i] * d.win[i]
	}
	d.tail += nsHop

	// всё, что дальше не получит перекрытия, можно выдавать
	ready := d.tail
	for i := 0; i < ready; i++ {
		v := d.outBuf[i] * 32768
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		d.outPCM = append(d.outPCM, int16(v))
	}
	d.outBuf = append(d.outBuf[:0], d.outBuf[ready:]...)
	d.tail = 0
}

// fft — итеративный радикс-2 Кули-Тьюки на месте. size обязан быть степенью 2.
// inverse=true делает обратное преобразование с нормировкой 1/N.
func fft(re, im []float64, inverse bool) {
	n := len(re)
	// бит-реверс перестановка
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
	}
	for length := 2; length <= n; length <<= 1 {
		ang := 2 * math.Pi / float64(length)
		if !inverse {
			ang = -ang
		}
		wr, wi := math.Cos(ang), math.Sin(ang)
		for i := 0; i < n; i += length {
			cr, ci := 1.0, 0.0
			for k := 0; k < length/2; k++ {
				ur, ui := re[i+k], im[i+k]
				vr := re[i+k+length/2]*cr - im[i+k+length/2]*ci
				vi := re[i+k+length/2]*ci + im[i+k+length/2]*cr
				re[i+k], im[i+k] = ur+vr, ui+vi
				re[i+k+length/2], im[i+k+length/2] = ur-vr, ui-vi
				cr, ci = cr*wr-ci*wi, cr*wi+ci*wr
			}
		}
	}
	if inverse {
		for i := range re {
			re[i] /= float64(n)
			im[i] /= float64(n)
		}
	}
}
