package agent

import "strings"

type SpecialistMode string

const (
	SpecialistCoding  SpecialistMode = "coding"
	SpecialistFinance SpecialistMode = "finance"
	SpecialistLegal   SpecialistMode = "legal"
	SpecialistGeneral SpecialistMode = "general"
)

var specialistPrompts = map[SpecialistMode]string{
	SpecialistCoding: `[حالت تخصصی: برنامه‌نویسی و مهندسی نرم‌افزار / Coding Specialist Mode]
- ساختار کد باید ماژولار، با پوشش کامل خطاها و مطابق الگوهای طراحی استاندارد باشد.
- تمام کدهای ایجاد شده باید تست‌پذیر باشند و پیش از اعلام پایان، اجرای آن‌ها بررسی شود.
- به کارایی حافظه، مدیریت منابع و عدم نشت اطلاعات حساس دقت ویژه داشته باشید.`,

	SpecialistFinance: `[حالت تخصصی: تحلیل مالی و اقتصادی / Finance Specialist Mode]
- تمام محاسبات مالی، ارزش‌گذاری‌ها و قیمت‌ها را با دقت ریالی و تومانی و تطبیق با نرخ‌های رسمی بازار انجام دهید.
- در تحلیل سهام و شرکت‌ها به شاخص‌های کلیدی بورس (P/E, EPS, حجم معاملات و صورت‌های مالی کدال) استناد کنید.
- همواره ریسک‌های سرمایه‌گذاری و نوسانات بازار را به صورت شفاف متذکر شوید.`,

	SpecialistLegal: `[حالت تخصصی: حقوقی و تنظیم قراردادها / Legal Specialist Mode]
- نگارش متون و قراردادها باید منطبق با قوانین جاری (قانون مدنی، قانون تجارت، قانون کار) و ادبیات رسمی حقوقی باشد.
- در تنظیم قراردادها، شروط ضمن عقد، مرجع حل اختلاف (داوری/دادگاه)، ضمانت اجرا و فورس ماژور را دقیق درج کنید.
- بر جنبه مشورتی تحلیل‌ها و لزوم بررسی نهایی توسط وکلای دادگستری تأکید فرمایید.`,
}

func GetSpecialistPrompt(mode string) string {
	m := SpecialistMode(strings.ToLower(strings.TrimSpace(mode)))
	if p, ok := specialistPrompts[m]; ok {
		return p
	}
	return ""
}
