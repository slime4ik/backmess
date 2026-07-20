package desktop

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

// errNoNativeDialog — системного диалога нет (нет zenity/kdialog на линуксе),
// вызывающий откатывается на встроенный фениевский.
var errNoNativeDialog = errors.New("нет системного диалога")

// Встроенный в Fyne файловый пикер выглядит чужеродно на всех трёх системах,
// поэтому сначала пробуем родной диалог ОС и только потом откатываемся.
// Отдельной зависимости не берём: везде хватает уже установленных утилит.

// nativeOpenDialog — «выбрать картинку». Пустая строка без ошибки = отмена.
func nativeOpenDialog() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return runDialog("osascript",
			"-e", `set f to choose file with prompt "выбери картинку" of type {"public.image"}`,
			"-e", "POSIX path of f")
	case "windows":
		return runDialog("powershell", "-NoProfile", "-STA", "-Command", `
Add-Type -AssemblyName System.Windows.Forms
$d = New-Object System.Windows.Forms.OpenFileDialog
$d.Filter = 'Картинки|*.png;*.jpg;*.jpeg;*.gif;*.webp'
if ($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { Write-Output $d.FileName }`)
	default:
		if path, err := exec.LookPath("zenity"); err == nil {
			return runDialog(path, "--file-selection", "--title=выбери картинку",
				"--file-filter=картинки | *.png *.jpg *.jpeg *.gif *.webp")
		}
		if path, err := exec.LookPath("kdialog"); err == nil {
			return runDialog(path, "--getopenfilename", ".",
				"image/png image/jpeg image/gif image/webp")
		}
		return "", errNoNativeDialog
	}
}

// nativeSaveDialog — «сохранить как». Пустая строка без ошибки = отмена.
func nativeSaveDialog(defaultName string) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return runDialog("osascript",
			"-e", `set f to choose file name with prompt "сохранить картинку" default name "`+escapeAS(defaultName)+`"`,
			"-e", "POSIX path of f")
	case "windows":
		return runDialog("powershell", "-NoProfile", "-STA", "-Command", `
Add-Type -AssemblyName System.Windows.Forms
$d = New-Object System.Windows.Forms.SaveFileDialog
$d.FileName = '`+escapePS(defaultName)+`'
if ($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { Write-Output $d.FileName }`)
	default:
		if path, err := exec.LookPath("zenity"); err == nil {
			return runDialog(path, "--file-selection", "--save", "--confirm-overwrite",
				"--filename="+defaultName)
		}
		if path, err := exec.LookPath("kdialog"); err == nil {
			return runDialog(path, "--getsavefilename", defaultName)
		}
		return "", errNoNativeDialog
	}
}

// runDialog запускает диалог и разбирает результат. Ненулевой код возврата у
// всех трёх систем означает «юзер нажал отмену» — это не ошибка.
func runDialog(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		var ee *exec.Error
		if errors.As(err, &ee) {
			return "", errNoNativeDialog // самой утилиты нет
		}
		return "", nil // отмена
	}
	return strings.TrimSpace(string(out)), nil
}

func escapeAS(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

func escapePS(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
