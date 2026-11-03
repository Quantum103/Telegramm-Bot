package bibletext

import "testing"

func TestBookIDResolvesCanonicalRussianAndEnglishNames(t *testing.T) {
	tests := []struct {
		name string
		want int
	}{
		{name: "Матфея", want: 40},
		{name: "Евангелие от Матфея", want: 40},
		{name: "Мф.", want: 40},
		{name: "1 Коринфянам", want: 46},
		{name: "Revelation", want: 66},
		{name: " Псалтирь ", want: 19},
	}
	for _, test := range tests {
		got, ok := BookID(test.name)
		if !ok || got != test.want {
			t.Errorf("BookID(%q) = %d, %t; want %d, true", test.name, got, ok, test.want)
		}
	}
}

func TestBookIDRejectsUnknownNames(t *testing.T) {
	for _, name := range []string{"", "неизвестная книга", "Матфея Евангелие 999"} {
		if id, ok := BookID(name); ok {
			t.Errorf("BookID(%q) = %d, true; want not found", name, id)
		}
	}
}

func TestIsSynodalTranslation(t *testing.T) {
	for _, value := range []string{"", "syn", "synodal", "Синодальный перевод"} {
		if !IsSynodalTranslation(value) {
			t.Errorf("IsSynodalTranslation(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"KJV", "NIV", "Перевод РБО"} {
		if IsSynodalTranslation(value) {
			t.Errorf("IsSynodalTranslation(%q) = true, want false", value)
		}
	}
}
