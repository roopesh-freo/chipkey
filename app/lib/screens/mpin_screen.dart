import 'package:flutter/material.dart';

import '../controller.dart';
import '../theme.dart';

/// A stand-in for the UPI PIN step. It checks nothing and sends nothing: any
/// four digits continue. In a real UPI app this page belongs to NPCI's
/// library, and the hardware key in this demo sits alongside it.
class MpinScreen extends StatefulWidget {
  const MpinScreen({super.key, required this.amount, required this.payee});
  final int amount;
  final String payee;

  @override
  State<MpinScreen> createState() => _MpinScreenState();
}

class _MpinScreenState extends State<MpinScreen> {
  static const _length = 4;
  var _digits = 0;

  void _tap(String key) {
    if (key == 'back') {
      if (_digits > 0) setState(() => _digits--);
      return;
    }
    if (_digits >= _length) return;
    setState(() => _digits++);
    // The digits themselves are never stored: only how many were typed.
    if (_digits == _length) Navigator.of(context).pop(true);
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: Colors.white,
      appBar: AppBar(
        leading: IconButton(
          key: const Key('pinClose'),
          icon: const Icon(Icons.close),
          onPressed: () => Navigator.of(context).pop(false),
        ),
        title: const Text(
          'Enter UPI PIN',
          style: TextStyle(fontSize: 18, fontWeight: FontWeight.w600),
        ),
      ),
      body: SafeArea(
        child: Column(
          children: [
            const SizedBox(height: 12),
            Text(
              'Paying ${widget.payee}',
              style: const TextStyle(color: Brand.grey, fontSize: 15),
            ),
            const SizedBox(height: 4),
            Text(
              rupees(widget.amount),
              style: const TextStyle(
                color: Brand.ink,
                fontSize: 38,
                fontWeight: FontWeight.w700,
              ),
            ),
            const SizedBox(height: 28),
            Row(
              key: const Key('pinDots'),
              mainAxisAlignment: MainAxisAlignment.center,
              children: [
                for (var i = 0; i < _length; i++)
                  Container(
                    width: 52,
                    height: 58,
                    margin: const EdgeInsets.symmetric(horizontal: 7),
                    alignment: Alignment.center,
                    decoration: BoxDecoration(
                      color: Brand.mist,
                      borderRadius: BorderRadius.circular(12),
                      border: Border.all(
                        color: i == _digits ? Brand.ink : Brand.line,
                        width: i == _digits ? 1.6 : 1,
                      ),
                    ),
                    child: i < _digits
                        ? Container(
                            width: 14,
                            height: 14,
                            decoration: const BoxDecoration(
                              color: Brand.ink,
                              shape: BoxShape.circle,
                            ),
                          )
                        : null,
                  ),
              ],
            ),
            const SizedBox(height: 16),
            const Padding(
              padding: EdgeInsets.symmetric(horizontal: 32),
              child: Text(
                'Demo PIN. Any 4 digits work. Nothing you type here is stored or sent.',
                textAlign: TextAlign.center,
                style: TextStyle(color: Brand.grey, fontSize: 12, height: 1.4),
              ),
            ),
            const Spacer(),
            Padding(
              padding: const EdgeInsets.fromLTRB(28, 0, 28, 12),
              child: Column(
                children: [
                  for (final row in const [
                    ['1', '2', '3'],
                    ['4', '5', '6'],
                    ['7', '8', '9'],
                    ['', '0', 'back'],
                  ])
                    Row(
                      children: [
                        for (final key in row)
                          Expanded(
                            child: key.isEmpty
                                ? const SizedBox(height: 64)
                                : InkWell(
                                    key: Key('pin-$key'),
                                    borderRadius: BorderRadius.circular(32),
                                    onTap: () => _tap(key),
                                    child: SizedBox(
                                      height: 64,
                                      child: Center(
                                        child: key == 'back'
                                            ? const Icon(
                                                Icons.backspace_outlined,
                                                color: Brand.ink,
                                              )
                                            : Text(
                                                key,
                                                style: const TextStyle(
                                                  color: Brand.ink,
                                                  fontSize: 26,
                                                  fontWeight: FontWeight.w500,
                                                ),
                                              ),
                                      ),
                                    ),
                                  ),
                          ),
                      ],
                    ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}
