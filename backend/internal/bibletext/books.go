package bibletext

import "strings"

// bookIDs maps Russian and English Bible book names and common abbreviations to the
// traditional 66-book order used by the supported Bible chapter sources.
var bookIDs = map[string]int{
	"бытие": 1, "быт": 1, "genesis": 1, "gen": 1,
	"исход": 2, "исх": 2, "exodus": 2, "ex": 2,
	"левит": 3, "лев": 3, "leviticus": 3, "lev": 3,
	"числа": 4, "чис": 4, "numbers": 4, "num": 4,
	"второзаконие": 5, "втор": 5, "deuteronomy": 5, "deut": 5,
	"иисус навин": 6, "нав": 6, "joshua": 6, "josh": 6,
	"судьи": 7, "суд": 7, "judges": 7, "judg": 7,
	"руфь": 8, "ruth": 8,
	"1 царств": 9, "1царств": 9, "1 самуила": 9, "1 samuel": 9, "1 sam": 9,
	"2 царств": 10, "2царств": 10, "2 самуила": 10, "2 samuel": 10, "2 sam": 10,
	"3 царств": 11, "3царств": 11, "1 царей": 11, "1 kings": 11,
	"4 царств": 12, "4царств": 12, "2 царей": 12, "2 kings": 12,
	"1 паралипоменон": 13, "1 пар": 13, "1 chronicles": 13, "1 chr": 13,
	"2 паралипоменон": 14, "2 пар": 14, "2 chronicles": 14, "2 chr": 14,
	"ездра": 15, "ezra": 15,
	"неемия": 16, "неем": 16, "nehemiah": 16, "neh": 16,
	"есфирь": 17, "ester": 17, "esther": 17, "est": 17,
	"иов": 18, "job": 18,
	"псалтирь": 19, "псалом": 19, "псалмы": 19, "пс": 19, "psalms": 19, "ps": 19,
	"притчи": 20, "притчи соломона": 20, "прит": 20, "proverbs": 20, "prov": 20,
	"екклесиаст": 21, "еккл": 21, "ecclesiastes": 21, "eccl": 21,
	"песня песней": 22, "песнь песней": 22, "песн": 22, "song of songs": 22,
	"исаия": 23, "исайя": 23, "ис": 23, "isaiah": 23, "isa": 23,
	"иеремия": 24, "иер": 24, "jeremiah": 24, "jer": 24,
	"плач иеремии": 25, "плач": 25, "lamentations": 25, "lam": 25,
	"иезекииль": 26, "иез": 26, "ezekiel": 26, "ezek": 26,
	"даниил": 27, "дан": 27, "daniel": 27, "dan": 27,
	"осия": 28, "ос": 28, "hosea": 28, "hos": 28,
	"иоиль": 29, "иоил": 29, "joel": 29,
	"амос": 30, "ам": 30, "amos": 30,
	"авдий": 31, "авд": 31, "obadiah": 31, "obad": 31,
	"иона": 32, "ион": 32, "jonah": 32, "jon": 32,
	"михей": 33, "мих": 33, "micah": 33, "mic": 33,
	"наум": 34, "nahum": 34, "nah": 34,
	"аввакум": 35, "авв": 35, "habakkuk": 35, "hab": 35,
	"софония": 36, "соф": 36, "zephaniah": 36, "zeph": 36,
	"аггей": 37, "агг": 37, "haggai": 37, "hag": 37,
	"захария": 38, "зах": 38, "zechariah": 38, "zech": 38,
	"малахия": 39, "мал": 39, "malachi": 39, "mal": 39,
	"матфея": 40, "от матфея": 40, "евангелие от матфея": 40, "мф": 40, "мат": 40, "matthew": 40, "matt": 40,
	"марка": 41, "от марка": 41, "евангелие от марка": 41, "мк": 41, "марк": 41, "mark": 41,
	"луки": 42, "от луки": 42, "евангелие от луки": 42, "лк": 42, "лук": 42, "luke": 42,
	"иоанна": 43, "от иоанна": 43, "евангелие от иоанна": 43, "ин": 43, "иоан": 43, "john": 43,
	"деяния": 44, "деяния апостолов": 44, "деян": 44, "acts": 44,
	"римлянам": 45, "послание к римлянам": 45, "рим": 45, "romans": 45, "rom": 45,
	"1 коринфянам": 46, "1 кор": 46, "1cor": 46, "1 corinthians": 46, "1 cor": 46,
	"2 коринфянам": 47, "2 кор": 47, "2cor": 47, "2 corinthians": 47, "2 cor": 47,
	"галатам": 48, "гал": 48, "galatians": 48, "gal": 48,
	"ефесянам": 49, "еф": 49, "ephesians": 49, "eph": 49,
	"филиппийцам": 50, "фил": 50, "philippians": 50, "phil": 50,
	"колоссянам": 51, "кол": 51, "colossians": 51, "col": 51,
	"1 фессалоникийцам": 52, "1 фес": 52, "1 thessalonians": 52, "1 thess": 52,
	"2 фессалоникийцам": 53, "2 фес": 53, "2 thessalonians": 53, "2 thess": 53,
	"1 тимофею": 54, "1 тим": 54, "1 timothy": 54, "1 tim": 54,
	"2 тимофею": 55, "2 тим": 55, "2 timothy": 55, "2 tim": 55,
	"титу": 56, "тит": 56, "titus": 56,
	"филимону": 57, "флм": 57, "philemon": 57, "phlm": 57,
	"евреям": 58, "послание к евреям": 58, "евр": 58, "hebrews": 58, "heb": 58,
	"иакова": 59, "послание иакова": 59, "иак": 59, "james": 59, "jas": 59,
	"1 петра": 60, "1 пет": 60, "1 peter": 60, "1 pet": 60,
	"2 петра": 61, "2 пет": 61, "2 peter": 61, "2 pet": 61,
	"1 иоанна": 62, "1 ин": 62, "1 john": 62,
	"2 иоанна": 63, "2 ин": 63, "2 john": 63,
	"3 иоанна": 64, "3 ин": 64, "3 john": 64,
	"иуды": 65, "послание иуды": 65, "иуд": 65, "jude": 65,
	"откровение": 66, "откровение иоанна богослова": 66, "откр": 66, "revelation": 66, "rev": 66,
}


// BookID resolves a book name or abbreviation to its canonical Bible book ID.
func BookID(value string) (int, bool) {
	value = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(value, "ё", "е")))
	value = strings.Trim(value, " .,:;")
	value = strings.Join(strings.Fields(value), " ")
	id, ok := bookIDs[value]
	return id, ok
}

// IsSynodalTranslation reports whether a passage explicitly requests the Synodal
// translation. An empty translation means the platform default (Synodal).
func IsSynodalTranslation(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "" || strings.Contains(value, "синод") || value == "syn" || value == "synodal"
}
