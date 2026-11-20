import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../backend.dart';
import '../controller.dart';
import '../theme.dart';
import 'demo_controls.dart';

/// The front door, on both nights. With the old app it is a number and an
/// OTP. With the chip the same number ends in a fingerprint on a phone the
/// account knows, and a phone it does not know has to wait to be let in.
class LoginScreen extends StatefulWidget {
  const LoginScreen({super.key, required this.controller, this.backend});
  final DemoController controller;
  final HttpBackend? backend;

  @override
  State<LoginScreen> createState() => _LoginScreenState();
}

class _LoginScreenState extends State<LoginScreen> {
  late final _phone = TextEditingController(text: _spaced(c.phone));
  final _otp = TextEditingController();
  String _draft = '';

  DemoController get c => widget.controller;

  static String _spaced(String n) =>
      n.length == 10 ? '${n.substring(0, 5)} ${n.substring(5)}' : n;

  @override
  void initState() {
    super.initState();
    c.addListener(_sync);
  }

  @override
  void dispose() {
    c.removeListener(_sync);
    _phone.dispose();
    _otp.dispose();
    super.dispose();
  }

  /// Copies a code picked from the SMS banner into the OTP field.
  void _sync() {
    if (c.otpDraft != _draft) {
      _draft = c.otpDraft;
      _otp.text = _draft;
    }
    if (c.stage != LoginStage.otp && _otp.text.isNotEmpty && _draft.isEmpty) {
      _otp.clear();
    }
  }

  String get _masked => '+91 ••${c.phone.substring(c.phone.length - 4)}';

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: Colors.black,
      body: SafeArea(
        child: LayoutBuilder(
          builder: (context, box) => SingleChildScrollView(
            child: ConstrainedBox(
              constraints: BoxConstraints(minHeight: box.maxHeight),
              child: IntrinsicHeight(
                child: Padding(
                  padding: const EdgeInsets.fromLTRB(24, 16, 24, 20),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      Row(
                        children: [
                          const Wordmark(color: Colors.white, size: 34),
                          const Spacer(),
                          IconButton(
                            key: const Key('demoControls'),
                            tooltip: 'Demo controls',
                            onPressed: () =>
                                showDemoControls(context, c, widget.backend),
                            icon: Icon(
                              Icons.tune,
                              color: c.role == Role.thief
                                  ? const Color(0xFFFF7070)
                                  : Brand.greyLight,
                            ),
                          ),
                        ],
                      ),
                      ...switch (c.stage) {
                        LoginStage.idle => _number(),
                        LoginStage.otp => _code(),
                        LoginStage.waiting => _waiting(),
                        LoginStage.refused => _refused(),
                        LoginStage.recovery => _recovery(),
                      },
                      const SizedBox(height: 12),
                      Text(
                        widget.backend == null ||
                                widget.backend!.baseUrl == defaultBackendUrl
                            ? 'A demo for flutterCon India 2026. This is not the real Freo app.'
                            : 'Demo app. Verifier at ${widget.backend!.baseUrl.replaceFirst('http://', '')}',
                        key: const Key('loginFootnote'),
                        textAlign: TextAlign.center,
                        style: const TextStyle(color: Brand.grey, fontSize: 11),
                      ),
                    ],
                  ),
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }

  // ---- stages --------------------------------------------------------------

  List<Widget> _number() {
    final bound = c.status.enrolled;
    return [
      const Spacer(flex: 2),
      const _Title('Hello!'),
      const _Subtitle('Log in to continue'),
      const SizedBox(height: 10),
      const _Body('Enter the mobile number on your account.'),
      const SizedBox(height: 28),
      TextField(
        key: const Key('phone'),
        controller: _phone,
        enabled: !c.busy,
        keyboardType: TextInputType.phone,
        inputFormatters: [FilteringTextInputFormatter.allow(RegExp(r'[0-9 ]'))],
        style: const TextStyle(
          color: Colors.white,
          fontSize: 20,
          fontWeight: FontWeight.w500,
          letterSpacing: 1,
        ),
        cursorColor: Colors.white,
        decoration: _field('Mobile number', prefix: '+91  '),
      ),
      const SizedBox(height: 14),
      _error(),
      const Spacer(flex: 3),
      Row(
        children: [
          Icon(
            bound
                ? Icons.verified_user_outlined
                : Icons.phonelink_lock_outlined,
            color: bound ? const Color(0xFF6CD164) : Brand.greyLight,
            size: 18,
          ),
          const SizedBox(width: 8),
          Expanded(
            child: Text(
              c.busy && c.phase.isNotEmpty
                  ? '${c.phase}…'
                  : bound
                  ? 'This phone holds a device key in ${(c.tier ?? 'hardware').toUpperCase()}.'
                  : 'This phone holds no device key yet.',
              key: const Key('loginStatus'),
              style: const TextStyle(color: Brand.greyLight, fontSize: 13),
            ),
          ),
        ],
      ),
      const SizedBox(height: 12),
      BrandButton(
        key: const Key('continue'),
        label: 'Continue',
        background: Colors.white,
        foreground: Colors.black,
        busy: c.busy,
        onPressed: () {
          FocusScope.of(context).unfocus();
          c.startLogin(_phone.text);
        },
      ),
    ];
  }

  List<Widget> _code() => [
    const Spacer(flex: 2),
    const _Title('Enter the OTP'),
    const SizedBox(height: 10),
    _Body('We sent a six-digit code by SMS to $_masked.'),
    const SizedBox(height: 28),
    TextField(
      key: const Key('otp'),
      controller: _otp,
      enabled: !c.busy,
      keyboardType: TextInputType.number,
      maxLength: 6,
      inputFormatters: [FilteringTextInputFormatter.digitsOnly],
      style: const TextStyle(
        color: Colors.white,
        fontSize: 28,
        fontWeight: FontWeight.w600,
        letterSpacing: 10,
      ),
      cursorColor: Colors.white,
      decoration: _field('OTP').copyWith(counterText: ''),
    ),
    const SizedBox(height: 14),
    _error(),
    const Spacer(flex: 3),
    if (c.busy && c.phase.isNotEmpty)
      Padding(
        padding: const EdgeInsets.only(bottom: 12),
        child: Text(
          '${c.phase}…',
          key: const Key('loginStatus'),
          style: const TextStyle(color: Brand.greyLight, fontSize: 13),
        ),
      ),
    BrandButton(
      key: const Key('verify'),
      label: 'Verify',
      background: Colors.white,
      foreground: Colors.black,
      busy: c.busy,
      onPressed: () {
        FocusScope.of(context).unfocus();
        c.submitOtp(_otp.text);
      },
    ),
    _link('changeNumber', 'Use a different number', c.backToStart),
  ];

  List<Widget> _waiting() => [
    const Spacer(flex: 2),
    const Icon(Icons.phonelink_ring_outlined, color: Colors.white, size: 44),
    const SizedBox(height: 18),
    const _Title('Check your other phone', key: Key('waitingTitle')),
    const SizedBox(height: 10),
    const _Body(
      'This account lives on another phone. We have asked it to approve this one with a fingerprint.',
    ),
    const SizedBox(height: 22),
    ClipRRect(
      borderRadius: BorderRadius.circular(4),
      child: const LinearProgressIndicator(
        minHeight: 4,
        color: Brand.coral,
        backgroundColor: Color(0xFF2A2A2A),
      ),
    ),
    const SizedBox(height: 22),
    const _Note(
      icon: Icons.sms_failed_outlined,
      text:
          'The OTP proved the number. It does not prove this phone, so it is not enough to get in.',
    ),
    const SizedBox(height: 14),
    _error(),
    const Spacer(flex: 3),
    _link('lost', 'I lost my phone', c.busy ? null : c.lostMyPhone),
    _link('cancelWait', 'Cancel', c.backToStart),
  ];

  List<Widget> _refused() => [
    const Spacer(flex: 2),
    const Icon(Icons.block, color: Color(0xFFFF7070), size: 48),
    const SizedBox(height: 18),
    const _Title('This phone was not let in', key: Key('refusedTitle')),
    const SizedBox(height: 10),
    _Body(c.refusedReason, key: const Key('refusedReason')),
    const SizedBox(height: 22),
    const _Note(
      icon: Icons.lock_outline,
      text:
          'Nothing on the account was shown to this phone, and nothing can be paid from it.',
    ),
    const SizedBox(height: 14),
    _error(),
    const Spacer(flex: 3),
    BrandButton(
      key: const Key('back'),
      label: 'Back to login',
      background: Colors.white,
      foreground: Colors.black,
      onPressed: c.backToStart,
    ),
    _link('lost', 'I lost my phone', c.busy ? null : c.lostMyPhone),
  ];

  List<Widget> _recovery() => [
    const Spacer(),
    const _Title('Recovery is slow, on purpose', key: Key('recoveryTitle')),
    const SizedBox(height: 10),
    const _Body('An OTP alone never lets a new phone into an account.'),
    const SizedBox(height: 22),
    const _Note(
      icon: Icons.face_retouching_natural,
      text: 'A live face check, matched to the photo from onboarding.',
    ),
    const SizedBox(height: 12),
    const _Note(
      icon: Icons.hourglass_bottom,
      text: 'Low limits for the first 24 hours on the new phone.',
    ),
    const SizedBox(height: 12),
    const _Note(
      icon: Icons.notifications_active_outlined,
      text: 'An alert to the old phone, email and SMS, with one tap to block.',
    ),
    const SizedBox(height: 12),
    const _Note(
      icon: Icons.key_off_outlined,
      text: 'The old key is revoked the moment the new one is live.',
    ),
    const Spacer(flex: 2),
    const Text(
      'The demo stops at this door.',
      textAlign: TextAlign.center,
      style: TextStyle(color: Brand.grey, fontSize: 12),
    ),
    const SizedBox(height: 10),
    BrandButton(
      key: const Key('back'),
      label: 'Back to login',
      background: Colors.white,
      foreground: Colors.black,
      onPressed: c.backToStart,
    ),
  ];

  // ---- pieces --------------------------------------------------------------

  Widget _error() => c.error == null
      ? const SizedBox.shrink()
      : Text(
          c.error!,
          key: const Key('loginError'),
          style: const TextStyle(
            color: Color(0xFFFF7070),
            fontSize: 14,
            height: 1.4,
          ),
        );

  Widget _link(String key, String label, VoidCallback? onPressed) => TextButton(
    key: Key(key),
    onPressed: onPressed,
    style: TextButton.styleFrom(
      foregroundColor: Colors.white,
      textStyle: const TextStyle(
        fontFamily: 'Poppins',
        fontSize: 14,
        fontWeight: FontWeight.w500,
      ),
    ),
    child: Text(label),
  );

  InputDecoration _field(String label, {String? prefix}) => InputDecoration(
    labelText: label,
    labelStyle: const TextStyle(color: Brand.greyLight),
    floatingLabelStyle: const TextStyle(color: Brand.greyLight),
    prefixText: prefix,
    prefixStyle: const TextStyle(color: Brand.greyLight, fontSize: 20),
    fillColor: const Color(0xFF1F1F1F),
    focusedBorder: UnderlineInputBorder(
      borderRadius: BorderRadius.circular(12),
      borderSide: const BorderSide(color: Colors.white, width: 2),
    ),
  );
}

class _Title extends StatelessWidget {
  const _Title(this.text, {super.key});
  final String text;

  @override
  Widget build(BuildContext context) => Text(
    text,
    style: const TextStyle(
      color: Colors.white,
      fontSize: 30,
      fontWeight: FontWeight.w700,
      height: 1.2,
    ),
  );
}

class _Subtitle extends StatelessWidget {
  const _Subtitle(this.text);
  final String text;

  @override
  Widget build(BuildContext context) => Text(
    text,
    style: const TextStyle(
      color: Colors.white,
      fontSize: 24,
      fontWeight: FontWeight.w500,
      height: 1.3,
    ),
  );
}

class _Body extends StatelessWidget {
  const _Body(this.text, {super.key});
  final String text;

  @override
  Widget build(BuildContext context) => Text(
    text,
    style: const TextStyle(color: Brand.greyLight, fontSize: 14, height: 1.45),
  );
}

class _Note extends StatelessWidget {
  const _Note({required this.icon, required this.text});
  final IconData icon;
  final String text;

  @override
  Widget build(BuildContext context) => Row(
    crossAxisAlignment: CrossAxisAlignment.start,
    children: [
      Icon(icon, color: Brand.greyLight, size: 20),
      const SizedBox(width: 12),
      Expanded(
        child: Text(
          text,
          style: const TextStyle(
            color: Colors.white,
            fontSize: 14,
            height: 1.4,
          ),
        ),
      ),
    ],
  );
}
