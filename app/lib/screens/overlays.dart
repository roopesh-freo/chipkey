import 'package:flutter/material.dart';

import '../controller.dart';
import '../theme.dart';

/// An SMS arriving, drawn like a notification at the top of the screen.
/// On Meera's phone it is her inbox. On the thief's phone it is the copy
/// her phone forwarded to him, and it says so.
class SmsBanner extends StatelessWidget {
  const SmsBanner({super.key, required this.controller});
  final DemoController controller;

  @override
  Widget build(BuildContext context) {
    final c = controller;
    final sms = c.banner;
    if (sms == null) return const SizedBox.shrink();
    final stolen = c.role == Role.thief;
    return SafeArea(
      bottom: false,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(10, 6, 10, 0),
        child: Material(
          key: const Key('smsBanner'),
          color: const Color(0xFF2B2B2B),
          elevation: 12,
          borderRadius: BorderRadius.circular(18),
          clipBehavior: Clip.antiAlias,
          child: InkWell(
            onTap: c.useBannerCode,
            child: Padding(
              padding: const EdgeInsets.fromLTRB(14, 12, 6, 12),
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Padding(
                    padding: const EdgeInsets.only(top: 2),
                    child: Icon(
                      stolen ? Icons.forward_to_inbox : Icons.sms_outlined,
                      color: stolen ? const Color(0xFFFF7070) : Colors.white,
                      size: 22,
                    ),
                  ),
                  const SizedBox(width: 12),
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          stolen
                              ? 'FORWARDED FROM HER PHONE · ${sms.at}'
                              : 'Messages · ${sms.at}',
                          key: const Key('smsFrom'),
                          style: TextStyle(
                            color: stolen
                                ? const Color(0xFFFF7070)
                                : Brand.greyLight,
                            fontSize: 11,
                            fontWeight: FontWeight.w600,
                            letterSpacing: 0.6,
                          ),
                        ),
                        const SizedBox(height: 3),
                        Text(
                          sms.text,
                          key: const Key('smsText'),
                          style: const TextStyle(
                            color: Colors.white,
                            fontSize: 14,
                            height: 1.35,
                          ),
                        ),
                        if (sms.code != null && c.stage == LoginStage.otp) ...[
                          const SizedBox(height: 4),
                          const Text(
                            'Tap to use this code',
                            style: TextStyle(
                              color: Brand.greyLight,
                              fontSize: 12,
                            ),
                          ),
                        ],
                      ],
                    ),
                  ),
                  IconButton(
                    key: const Key('smsDismiss'),
                    onPressed: c.dismissBanner,
                    visualDensity: VisualDensity.compact,
                    icon: const Icon(
                      Icons.close,
                      color: Brand.greyLight,
                      size: 18,
                    ),
                  ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }
}

/// The old phone being asked to let a new phone in. Approving costs a
/// fingerprint, because the chip signs the answer. Blocking costs nothing.
class ApprovalCard extends StatelessWidget {
  const ApprovalCard({super.key, required this.controller});
  final DemoController controller;

  @override
  Widget build(BuildContext context) {
    final c = controller;
    final request = c.incoming;
    if (request == null) return const SizedBox.shrink();
    return Material(
      color: Colors.black.withValues(alpha: 0.6),
      child: Align(
        alignment: Alignment.bottomCenter,
        child: Container(
          key: const Key('approvalCard'),
          width: double.infinity,
          decoration: const BoxDecoration(
            color: Colors.white,
            borderRadius: BorderRadius.vertical(top: Radius.circular(24)),
          ),
          child: SafeArea(
            top: false,
            child: Padding(
              padding: const EdgeInsets.fromLTRB(22, 22, 22, 18),
              child: Column(
                mainAxisSize: MainAxisSize.min,
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Row(
                    children: [
                      Container(
                        padding: const EdgeInsets.all(10),
                        decoration: const BoxDecoration(
                          color: Brand.coralTint,
                          shape: BoxShape.circle,
                        ),
                        child: const Icon(
                          Icons.phonelink_lock_outlined,
                          color: Brand.coralDark,
                        ),
                      ),
                      const SizedBox(width: 12),
                      const Expanded(
                        child: Text(
                          'A new phone wants into your account',
                          style: TextStyle(
                            color: Brand.ink,
                            fontSize: 19,
                            fontWeight: FontWeight.w700,
                            height: 1.25,
                          ),
                        ),
                      ),
                    ],
                  ),
                  const SizedBox(height: 16),
                  Container(
                    padding: const EdgeInsets.all(14),
                    decoration: BoxDecoration(
                      color: Brand.mist,
                      borderRadius: BorderRadius.circular(14),
                    ),
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          request.deviceName,
                          key: const Key('approvalDevice'),
                          style: const TextStyle(
                            color: Brand.ink,
                            fontSize: 17,
                            fontWeight: FontWeight.w600,
                          ),
                        ),
                        const SizedBox(height: 2),
                        Text(
                          'Asked at ${request.requestedAt} · key in ${request.tier.toUpperCase()}',
                          style: const TextStyle(
                            color: Brand.grey,
                            fontSize: 13,
                          ),
                        ),
                      ],
                    ),
                  ),
                  const SizedBox(height: 12),
                  const Text(
                    'It has the right OTP. That only proves the number. If this is not you, block it.',
                    style: TextStyle(
                      color: Brand.grey,
                      fontSize: 13,
                      height: 1.4,
                    ),
                  ),
                  if (c.error != null) ...[
                    const SizedBox(height: 8),
                    Text(
                      c.error!,
                      key: const Key('approvalError'),
                      style: const TextStyle(color: Brand.red, fontSize: 13),
                    ),
                  ],
                  const SizedBox(height: 16),
                  BrandButton(
                    key: const Key('approve'),
                    label: 'Approve with fingerprint',
                    busy: c.busy,
                    onPressed: c.approveIncoming,
                  ),
                  const SizedBox(height: 8),
                  SizedBox(
                    height: 50,
                    child: OutlinedButton(
                      key: const Key('block'),
                      onPressed: c.busy ? null : c.blockIncoming,
                      style: OutlinedButton.styleFrom(
                        foregroundColor: Brand.red,
                        side: const BorderSide(color: Brand.red),
                        shape: RoundedRectangleBorder(
                          borderRadius: BorderRadius.circular(12),
                        ),
                        textStyle: const TextStyle(
                          fontFamily: 'Poppins',
                          fontSize: 16,
                          fontWeight: FontWeight.w600,
                        ),
                      ),
                      child: const Text('Block this phone'),
                    ),
                  ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }
}
