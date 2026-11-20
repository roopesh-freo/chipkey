import 'package:flutter/material.dart';

/// Colours in the style of the Freo app: black, white and one coral accent.
abstract final class Brand {
  static const coral = Color(0xFFF04057);
  static const coralDark = Color(0xFFCC364A);
  static const coralTint = Color(0xFFFEEBEE);
  static const ink = Color(0xFF191919);
  static const grey = Color(0xFF737373);
  static const greyLight = Color(0xFFB2B2B2);
  static const line = Color(0xFFD9D9D9);
  static const mist = Color(0xFFF5F5F5);
  static const green = Color(0xFF32A529);
  static const greenTint = Color(0xFFEBF9EA);
  static const red = Color(0xFFD93636);
  static const redTint = Color(0xFFFFEBEB);
  static const amber = Color(0xFFD9AC02);
  static const amberTint = Color(0xFFFFFAE5);
}

ThemeData brandTheme() {
  final base = ThemeData(
    brightness: Brightness.light,
    fontFamily: 'Poppins',
    scaffoldBackgroundColor: Brand.mist,
    colorScheme: const ColorScheme.light(
      primary: Brand.coral,
      onPrimary: Colors.white,
      secondary: Brand.ink,
      surface: Colors.white,
      onSurface: Brand.ink,
      error: Brand.red,
    ),
  );
  return base.copyWith(
    appBarTheme: const AppBarTheme(
      backgroundColor: Colors.white,
      foregroundColor: Brand.ink,
      elevation: 0,
      scrolledUnderElevation: 0,
      centerTitle: false,
    ),
    inputDecorationTheme: InputDecorationTheme(
      filled: true,
      fillColor: Brand.mist,
      labelStyle: const TextStyle(color: Brand.grey),
      floatingLabelStyle: const TextStyle(color: Brand.ink),
      contentPadding: const EdgeInsets.fromLTRB(14, 10, 14, 10),
      // An underline border with no line keeps the label inside the filled box.
      border: UnderlineInputBorder(
        borderRadius: BorderRadius.circular(12),
        borderSide: BorderSide.none,
      ),
      enabledBorder: UnderlineInputBorder(
        borderRadius: BorderRadius.circular(12),
        borderSide: BorderSide.none,
      ),
      focusedBorder: UnderlineInputBorder(
        borderRadius: BorderRadius.circular(12),
        borderSide: const BorderSide(color: Brand.coral, width: 2),
      ),
    ),
  );
}

/// The wordmark, set in type. It is a stand-in, not the real logo.
class Wordmark extends StatelessWidget {
  const Wordmark({super.key, this.color = Brand.ink, this.size = 30});
  final Color color;
  final double size;

  @override
  Widget build(BuildContext context) {
    return Row(
      mainAxisSize: MainAxisSize.min,
      crossAxisAlignment: CrossAxisAlignment.center,
      children: [
        Text.rich(
          TextSpan(
            children: [
              TextSpan(text: 'freo'),
              const TextSpan(
                text: '.',
                style: TextStyle(color: Brand.coral),
              ),
            ],
          ),
          style: TextStyle(
            color: color,
            fontSize: size,
            fontWeight: FontWeight.w700,
            letterSpacing: -1,
            height: 1,
          ),
        ),
        const SizedBox(width: 8),
        Container(
          padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
          decoration: BoxDecoration(
            border: Border.all(color: Brand.coral),
            borderRadius: BorderRadius.circular(4),
          ),
          child: const Text(
            'DEMO',
            style: TextStyle(
              color: Brand.coral,
              fontSize: 10,
              fontWeight: FontWeight.w600,
              letterSpacing: 1.2,
            ),
          ),
        ),
      ],
    );
  }
}

/// The 54-point call-to-action button used on every screen.
class BrandButton extends StatelessWidget {
  const BrandButton({
    super.key,
    required this.label,
    required this.onPressed,
    this.background = Brand.coral,
    this.foreground = Colors.white,
    this.busy = false,
  });

  final String label;
  final VoidCallback? onPressed;
  final Color background;
  final Color foreground;
  final bool busy;

  @override
  Widget build(BuildContext context) {
    return SizedBox(
      height: 54,
      width: double.infinity,
      child: FilledButton(
        onPressed: busy ? null : onPressed,
        style: FilledButton.styleFrom(
          backgroundColor: background,
          foregroundColor: foreground,
          disabledBackgroundColor: background.withValues(
            alpha: busy ? 1 : 0.45,
          ),
          disabledForegroundColor: foreground.withValues(alpha: busy ? 1 : 0.7),
          shape: RoundedRectangleBorder(
            borderRadius: BorderRadius.circular(12),
          ),
          textStyle: const TextStyle(
            fontFamily: 'Poppins',
            fontSize: 16,
            fontWeight: FontWeight.w600,
          ),
        ),
        child: busy
            ? SizedBox(
                width: 22,
                height: 22,
                child: CircularProgressIndicator(
                  strokeWidth: 2.4,
                  color: foreground,
                ),
              )
            : Text(label),
      ),
    );
  }
}

/// A small coloured label, used for the device key tier.
class Pill extends StatelessWidget {
  const Pill({
    super.key,
    required this.icon,
    required this.text,
    required this.color,
    required this.background,
  });
  final IconData icon;
  final String text;
  final Color color;
  final Color background;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 5),
      decoration: BoxDecoration(
        color: background,
        borderRadius: BorderRadius.circular(20),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(icon, size: 15, color: color),
          const SizedBox(width: 6),
          Flexible(
            child: Text(
              text,
              overflow: TextOverflow.ellipsis,
              style: TextStyle(
                color: color,
                fontSize: 12,
                fontWeight: FontWeight.w600,
              ),
            ),
          ),
        ],
      ),
    );
  }
}
